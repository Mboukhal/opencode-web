package runner

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"opencodeweb/event"
	"opencodeweb/opencode"
)

// streamServer answers /api/session, /api/session/{id}/prompt and streams
// whatever SSE frames the test gives it on /api/event.
func streamServer(t *testing.T, frames ...string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/session", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":{"id":"ses_t","projectID":"p","time":{"created":1,"updated":1},"location":{"directory":"/d"}}}`))
	})
	mux.HandleFunc("POST /api/session/{id}/prompt", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":{"id":"m"}}`))
	})
	mux.HandleFunc("GET /api/event", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, f := range frames {
			w.Write([]byte("data: " + f + "\n\n"))
			w.(http.Flusher).Flush()
		}
		// Keep the stream open, like the real server does.
		<-r.Context().Done()
	})
	return httptest.NewServer(mux)
}

// TestFailedTurnEndsJob guards the regression where a provider failure left the
// run waiting on an event stream that never closes.
func TestFailedTurnEndsJob(t *testing.T) {
	srv := streamServer(t,
		`{"type":"session.text.delta","data":{"sessionID":"ses_t","ordinal":0,"delta":"hi"}}`,
		`{"type":"session.execution.failed","data":{"sessionID":"ses_t","error":{"type":"provider.auth","message":"User not found."}}}`,
	)
	defer srv.Close()

	client := opencode.New(srv.URL, "")
	m := New(func() *opencode.Client { return client }, 10*time.Second)

	job, err := m.Start(context.Background(), "o", "r", "/d", "hello", "")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	var got []event.Event
	_, ch, unsub := job.Subscribe()
	defer unsub()

	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev := <-ch:
			got = append(got, ev)
			if ev.Type == "done" {
				if ev.Error == "" {
					t.Fatal("done event carried no error")
				}
				return
			}
		case <-deadline:
			t.Fatalf("job did not finish; events = %+v", got)
		}
	}
}

func TestTitleFromPrompt(t *testing.T) {
	if got := title("fix the parser\nthen run tests"); got != "fix the parser" {
		t.Errorf("title = %q", got)
	}
	long := make([]byte, 200)
	for i := range long {
		long[i] = 'a'
	}
	got := title(string(long))
	if n := len([]rune(got)); n != 81 {
		t.Errorf("long title = %d runes, want 81 (80 + ellipsis)", n)
	}
}
