// Package runner drives the OpenCode CLI: it launches a prompt inside a project
// directory, streams events, and exposes session history.
package runner

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"opencodeweb/event"
	"opencodeweb/ocjson"
)

// Job is one running prompt.
type Job struct {
	ID        string
	Owner     string
	Repo      string
	Dir       string
	Prompt    string
	SessionID string

	mu        sync.Mutex
	listeners map[chan event.Event]struct{}
	buffer    []event.Event
	done      bool
	err       error
	startedAt time.Time
	cancel    context.CancelFunc
}

// Manager launches jobs and keeps recent history.
type Manager struct {
	bin      string
	cfgDir   string
	stateDir string
	model    string
	agent    string
	timeout  time.Duration
	home     string

	runnerEnv []string

	mu   sync.Mutex
	jobs map[string]*Job
}

// New creates a Runner manager.
func New(bin, cfgDir, stateDir, model, agent, home string, timeout time.Duration, runnerEnv map[string]string) *Manager {
	env := make([]string, 0, len(runnerEnv))
	for k, v := range runnerEnv {
		env = append(env, k+"="+v)
	}
	sort.Strings(env)
	return &Manager{
		bin: bin, cfgDir: cfgDir, stateDir: stateDir, model: model,
		agent: agent, timeout: timeout, home: home, jobs: make(map[string]*Job),
		runnerEnv: env,
	}
}

// Start launches a prompt for a project. sessionID continues an existing session.
func (m *Manager) Start(parent context.Context, owner, repo, dir, prompt, sessionID, projectModel, projectAgent string) (*Job, error) {
	args := []string{"run", "--standalone", "--format", "json", "--auto"}
	if sessionID != "" {
		args = append(args, "--session", sessionID)
	}
	model := firstNonEmpty(projectModel, m.model)
	if model != "" {
		args = append(args, "-m", model)
	}
	if agent := firstNonEmpty(projectAgent, m.agent); agent != "" {
		args = append(args, "--agent", agent)
	}
	args = append(args, prompt)

	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), m.timeout)

	cmd := exec.Command(m.bin, args...)
	cmd.Dir = dir
	cmd.Env = m.childEnv(dir)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("starting opencode: %w", err)
	}
	slog.Info("opencode started", "bin", m.bin, "dir", dir, "args", strings.Join(args[:len(args)-1], " "))

	// exec.Command has no timeout of its own: kill the CLI when the deadline
	// passes or the job is cancelled from the UI.
	go func() {
		<-ctx.Done()
		if cmd.Process != nil {
			cmd.Process.Kill()
		}
	}()

	job := &Job{
		ID: uuid.NewString(), Owner: owner, Repo: repo, Dir: dir, Prompt: prompt,
		SessionID: sessionID, listeners: make(map[chan event.Event]struct{}),
		startedAt: time.Now(), cancel: cancel,
	}
	m.mu.Lock()
	m.jobs[job.ID] = job
	m.mu.Unlock()

	go job.pump(ctx, stdout, stderr, cmd)
	go func() {
		<-ctx.Done()
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			job.finish(fmt.Errorf("timed out after %s", m.timeout))
		}
	}()
	return job, nil
}

// Job looks up a running job.
func (m *Manager) Job(id string) *Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.jobs[id]
}

func (j *Job) pump(ctx context.Context, stdout, stderr io.Reader, cmd *exec.Cmd) {
	var wg sync.WaitGroup
	var errBuf strings.Builder

	wg.Go(func() {
		s := bufio.NewScanner(stdout)
		s.Buffer(make([]byte, 64*1024), 4*1024*1024)
		for s.Scan() {
			if ev, ok := ocjson.Parse(s.Text()); ok {
				j.setSession(ev.SessionID)
				j.publish(ev.Event)
			}
		}
	})
	wg.Go(func() {
		s := bufio.NewScanner(stderr)
		s.Buffer(make([]byte, 64*1024), 1<<20)
		for s.Scan() {
			line := s.Text()
			if strings.HasPrefix(line, "timestamp=") {
				continue
			}
			if errBuf.Len() < 4096 {
				errBuf.WriteString(line)
				errBuf.WriteByte('\n')
			}
		}
	})
	wg.Wait()
	err := cmd.Wait()
	if err != nil && errBuf.Len() > 0 {
		slog.Error("opencode failed", "job", j.ID, "err", err, "stderr", strings.TrimSpace(errBuf.String()))
	} else if err != nil {
		slog.Error("opencode failed", "job", j.ID, "err", err)
	}
	if err != nil && errBuf.Len() > 0 {
		err = fmt.Errorf("%w: %s", err, firstLine(errBuf.String()))
	}
	j.finish(err)
}

// Session reports the OpenCode session id discovered while running.
func (j *Job) Session() string {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.SessionID
}

func (j *Job) setSession(id string) {
	if id == "" {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.SessionID == "" {
		j.SessionID = id
	}
}

func (j *Job) publish(ev event.Event) {
	j.mu.Lock()
	if j.done {
		j.mu.Unlock()
		return
	}
	j.buffer = append(j.buffer, ev)
	if len(j.buffer) > 2000 {
		j.buffer = j.buffer[len(j.buffer)-2000:]
	}
	listeners := make([]chan event.Event, 0, len(j.listeners))
	for c := range j.listeners {
		listeners = append(listeners, c)
	}
	j.mu.Unlock()

	for _, c := range listeners {
		select {
		case c <- ev:
		default: // slow subscriber; it can replay from the buffer
		}
	}
}

func (j *Job) finish(err error) {
	j.mu.Lock()
	if j.done {
		j.mu.Unlock()
		return
	}
	j.done = true
	j.err = err
	ev := event.Event{Type: "done"}
	if err != nil {
		ev.Error = err.Error()
	}
	listeners := make([]chan event.Event, 0, len(j.listeners))
	for c := range j.listeners {
		listeners = append(listeners, c)
	}
	j.mu.Unlock()

	for _, c := range listeners {
		select {
		case c <- ev:
		default:
		}
	}
	j.cancel()
}

// Subscribe returns buffered events and a channel of new ones.
func (j *Job) Subscribe() ([]event.Event, <-chan event.Event, func()) {
	ch := make(chan event.Event, 256)
	j.mu.Lock()
	history := append([]event.Event(nil), j.buffer...)
	if j.done {
		ev := event.Event{Type: "done"}
		if j.err != nil {
			ev.Error = j.err.Error()
		}
		history = append(history, ev)
		j.mu.Unlock()
		close(ch)
		return history, ch, func() {}
	}
	j.listeners[ch] = struct{}{}
	j.mu.Unlock()
	return history, ch, func() {
		j.mu.Lock()
		delete(j.listeners, ch)
		j.mu.Unlock()
	}
}

// Cancel stops the underlying CLI process.
func (j *Job) Cancel() { j.cancel() }

// StartedAt reports when the job began.
func (j *Job) StartedAt() time.Time { return j.startedAt }

func (m *Manager) childEnv(dir string) []string {
	return append(cleanEnv(), append(m.runnerEnv,
		"HOME="+m.home,
		"PWD="+dir, // OpenCode resolves project .opencode/ from PWD, not cwd
		"XDG_CONFIG_HOME="+m.cfgDir,
		"XDG_DATA_HOME="+m.stateDir,
		"XDG_STATE_HOME="+m.stateDir,
		"OPENCODE_CONFIG_DIR="+m.cfgDir,
		"TERM=dumb",
		"CI=1",
	)...)
}

// cleanEnv drops server credentials so prompts never see the OAuth secret.
func cleanEnv() []string {
	var out []string
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		switch name {
		case "GOOGLE_CLIENT_ID", "GOOGLE_CLIENT_SECRET", "ALLOWED_EMAILS", "PUBLIC_BASE_URL":
			continue
		}
		out = append(out, kv)
	}
	return out
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
