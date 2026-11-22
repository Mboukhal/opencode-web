// Package runner drives prompts through the shared OpenCode server: it creates
// or resumes a session in the project directory, sends the prompt, and relays
// the server's event stream to the browser.
//
// Going through the shared server (the same one OpenCode Mobile pairs with)
// means a session started here keeps running on this machine and can be opened
// from the phone, and vice versa.
package runner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"

	"opencodeweb/event"
	"opencodeweb/opencode"
)

// Job is one running prompt.
type Job struct {
	ID        string
	Owner     string
	Repo      string
	Dir       string
	Prompt    string
	SessionID string
	StartedAt time.Time

	terminal chan struct{} // closed when the turn ends on the server
	termOnce sync.Once

	mu        sync.Mutex
	listeners map[chan event.Event]struct{}
	buffer    []event.Event
	done      bool
	err       error
	failMsg   string
	cancel    context.CancelFunc
	client    *opencode.Client
}

// Manager launches prompts against the OpenCode server.
type Manager struct {
	// ClientFn resolves the OpenCode client. It is a function so the manager can
	// be constructed before the background server has finished starting.
	ClientFn func() *opencode.Client
	Timeout  time.Duration

	mu   sync.Mutex
	jobs map[string]*Job
}

// New creates a Runner manager bound to a client provider.
func New(client func() *opencode.Client, timeout time.Duration) *Manager {
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}
	return &Manager{ClientFn: client, Timeout: timeout, jobs: make(map[string]*Job)}
}

// Start creates a job, opens (or resumes) the session, and sends the prompt.
// sessionID continues an existing session; empty starts a new one titled with
// the first line of the prompt.
func (m *Manager) Start(ctx context.Context, owner, repo, dir, prompt, sessionID string) (*Job, error) {
	client := m.Client()
	if client == nil {
		return nil, errors.New("the OpenCode server is not running yet")
	}

	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), m.Timeout)

	if sessionID == "" {
		s, err := client.CreateSession(ctx, dir, title(prompt))
		if err != nil {
			cancel()
			return nil, fmt.Errorf("creating session: %w", err)
		}
		sessionID = s.ID
	}

	job := &Job{
		ID: uuid.NewString(), Owner: owner, Repo: repo, Dir: dir, Prompt: prompt,
		SessionID: sessionID, listeners: make(map[chan event.Event]struct{}),
		StartedAt: time.Now(), cancel: cancel, client: client,
		terminal: make(chan struct{}),
	}
	m.mu.Lock()
	m.jobs[job.ID] = job
	m.mu.Unlock()

	go m.run(ctx, job, client)
	return job, nil
}

// run subscribes to events, then submits the prompt so no early output is lost.
func (m *Manager) run(ctx context.Context, job *Job, client *opencode.Client) {
	defer job.cancel()

	// Subscribe first: the server only emits execution events after we are
	// listening, which avoids a race where the first tokens are missed.
	drained := make(chan error, 1)
	streamCtx, stopStream := context.WithCancel(ctx)
	defer stopStream()
	go func() {
		drained <- client.Drain(streamCtx, job.SessionID, job.publish)
	}()

	// Give the subscription a moment to attach before the prompt is admitted.
	time.Sleep(150 * time.Millisecond)

	if err := client.Prompt(ctx, job.SessionID, job.Prompt); err != nil {
		stopStream()
		job.finish(fmt.Errorf("sending prompt: %w", err))
		return
	}

	// The event stream stays open for every session on the server, so the job
	// cannot wait for it to close. terminal is closed when the turn ends.
	select {
	case <-ctx.Done():
		job.finish(ctx.Err())
	case <-job.terminal:
		stopStream()
		job.finish(job.failure())
	case err := <-drained:
		if err != nil && !errors.Is(err, context.Canceled) {
			slog.Warn("event stream ended", "job", job.ID, "err", err)
			job.finish(fmt.Errorf("event stream ended: %w", err))
			return
		}
		job.finish(job.failure())
	}
}

// Client returns the live OpenCode client, or nil before the server starts.
func (m *Manager) Client() *opencode.Client {
	if m.ClientFn == nil {
		return nil
	}
	return m.ClientFn()
}

// Job looks up a running job.
func (m *Manager) Job(id string) *Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.jobs[id]
}

// Session reports the OpenCode session id.
func (j *Job) Session() string {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.SessionID
}

func (j *Job) publish(ev event.Event) {
	// Errors carry the failure message; the run still has to be marked done so
	// the UI stops waiting for a turn the server already abandoned.
	if ev.Type == "error" && ev.Error != "" {
		j.mu.Lock()
		if j.failMsg == "" {
			j.failMsg = ev.Error
		}
		j.mu.Unlock()
	}

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

	if ev.Type == "done" || ev.Type == "error" {
		j.termOnce.Do(func() { close(j.terminal) })
	}
}

// failure reports the error recorded while the turn ran, if any.
func (j *Job) failure() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.failMsg == "" {
		return nil
	}
	return errors.New(j.failMsg)
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

// Cancel stops this job and interrupts the running turn on the server.
func (j *Job) Cancel() {
	j.cancel()
	if j.client != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = j.client.Interrupt(ctx, j.Session())
	}
}

// StartedAt reports when the job began (kept as a method for templates).
func (j *Job) Started() time.Time { return j.StartedAt }

// Err reports the failure, if any.
func (j *Job) Err() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.err
}

func title(prompt string) string {
	s := prompt
	if i := indexByte(s, '\n'); i > 0 {
		s = s[:i]
	}
	if len(s) > 80 {
		s = s[:80] + "…"
	}
	if s == "" {
		return "OpenCode session"
	}
	return s
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}
