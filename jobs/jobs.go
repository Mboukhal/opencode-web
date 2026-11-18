// Package jobs tracks long-running git operations (clone, pull) so the browser
// can follow their progress over SSE.
package jobs

import (
	"sync"
	"time"

	"github.com/google/uuid"
)

// Kind is the operation a job performs.
type Kind string

const (
	KindClone Kind = "clone"
	KindPull  Kind = "pull"
)

// State is the lifecycle of a job.
type State string

const (
	StateRunning State = "running"
	StateDone    State = "done"
	StateFailed  State = "failed"
)

// Event is one progress line for a job.
type Event struct {
	Type    string `json:"type"` // "log" | "state"
	Message string `json:"message,omitempty"`
	State   State  `json:"state,omitempty"`
	Error   string `json:"error,omitempty"`
}

// Job is one clone or pull.
type Job struct {
	ID    string
	Kind  Kind
	Owner string
	Repo  string

	mu        sync.Mutex
	state     State
	err       string
	lines     []Event
	listeners map[chan Event]struct{}
	started   time.Time
	ended     time.Time
}

// Manager keeps recent jobs in memory.
type Manager struct {
	mu   sync.Mutex
	jobs map[string]*Job
	// active maps owner/repo to the running job id.
	active map[string]string
}

// New creates a job manager.
func New() *Manager {
	return &Manager{jobs: make(map[string]*Job), active: make(map[string]string)}
}

// Start registers a new running job for owner/repo.
func (m *Manager) Start(kind Kind, owner, repo string) *Job {
	j := &Job{
		ID: uuid.NewString(), Kind: kind, Owner: owner, Repo: repo,
		state: StateRunning, listeners: make(map[chan Event]struct{}), started: time.Now(),
	}
	m.mu.Lock()
	m.jobs[j.ID] = j
	m.active[owner+"/"+repo] = j.ID
	m.mu.Unlock()
	return j
}

// Get returns a job by id.
func (m *Manager) Get(id string) *Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.jobs[id]
}

// ActiveFor returns the running job for a repo, or nil.
func (m *Manager) ActiveFor(owner, repo string) *Job {
	m.mu.Lock()
	id, ok := m.active[owner+"/"+repo]
	m.mu.Unlock()
	if !ok {
		return nil
	}
	j := m.Get(id)
	if j == nil || j.State() != StateRunning {
		return nil
	}
	return j
}

// Active lists every running job.
func (m *Manager) Active() []*Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*Job
	for _, j := range m.jobs {
		if j.State() == StateRunning {
			out = append(out, j)
		}
	}
	return out
}

// Log appends a progress line.
func (j *Job) Log(msg string) { j.emit(Event{Type: "log", Message: msg}) }

// Finish marks the job done or failed.
func (j *Job) Finish(err error) {
	j.mu.Lock()
	if j.state != StateRunning {
		j.mu.Unlock()
		return
	}
	j.ended = time.Now()
	ev := Event{Type: "state", State: StateDone}
	if err != nil {
		j.state = StateFailed
		j.err = err.Error()
		ev = Event{Type: "state", State: StateFailed, Error: err.Error()}
	} else {
		j.state = StateDone
	}
	j.mu.Unlock()
	j.emit(ev)
}

// State reports the current state.
func (j *Job) State() State {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.state
}

// Err reports the failure message, if any.
func (j *Job) Err() string {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.err
}

// Elapsed is how long the job has been running.
func (j *Job) Elapsed() time.Duration {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.state == StateRunning {
		return time.Since(j.started)
	}
	return j.ended.Sub(j.started)
}

func (j *Job) emit(ev Event) {
	j.mu.Lock()
	j.lines = append(j.lines, ev)
	if len(j.lines) > 500 {
		j.lines = j.lines[len(j.lines)-500:]
	}
	listeners := make([]chan Event, 0, len(j.listeners))
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

// Subscribe returns buffered events and a channel of new ones. The channel is
// closed once the job has finished and all events were delivered.
func (j *Job) Subscribe() ([]Event, <-chan Event, func()) {
	ch := make(chan Event, 128)
	j.mu.Lock()
	history := append([]Event(nil), j.lines...)
	if j.state != StateRunning {
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
