package web

import (
	"time"

	"opencodeweb/runner"
)

// runData is the template view of a running job.
type runData struct {
	ID        string
	Owner     string
	Repo      string
	Prompt    string
	StartedAt time.Time
}

func newRunData(j *runner.Job) runData {
	return runData{ID: j.ID, Owner: j.Owner, Repo: j.Repo, Prompt: j.Prompt, StartedAt: j.StartedAt()}
}
