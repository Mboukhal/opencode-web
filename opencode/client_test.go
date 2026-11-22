package opencode

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"opencodeweb/event"
)

// fakeServer answers the routes the client uses and asserts basic auth.
func fakeServer(t *testing.T, password string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	requireAuth := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			u, p, ok := r.BasicAuth()
			if !ok || u != "opencode" || p != password {
				w.WriteHeader(http.StatusUnauthorized)
				w.Write([]byte(`{"_tag":"UnauthorizedError","message":"Authentication required"}`))
				return
			}
			next(w, r)
		}
	}

	mux.HandleFunc("GET /api/info", requireAuth(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"version": "2.3.2", "urls": []string{"http://127.0.0.1:4096"}})
	}))

	mux.HandleFunc("POST /api/session", requireAuth(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Location Location `json:"location"`
			Title    string   `json:"title"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		if body.Location.Directory == "" {
			t.Error("create session did not carry a location directory")
		}
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"id": "ses_new", "title": body.Title, "projectID": "p1",
				"time":     map[string]any{"created": 1, "updated": 2},
				"location": map[string]any{"directory": body.Location.Directory},
			},
		})
	}))

	mux.HandleFunc("GET /api/session", requireAuth(func(w http.ResponseWriter, r *http.Request) {
		dir := r.URL.Query().Get("directory")
		json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"id": "ses_a", "title": "mine", "time": map[string]any{"created": 1, "updated": 5},
					"location": map[string]any{"directory": dir}},
			},
			"cursor": map[string]any{},
		})
	}))

	mux.HandleFunc("POST /api/session/{id}/prompt", requireAuth(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Text string `json:"text"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		if body.Text == "" {
			t.Error("prompt text was empty")
		}
		json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"id": "msg_1", "type": "user"}})
	}))

	mux.HandleFunc("POST /api/credential", requireAuth(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			IntegrationID string `json:"integrationID"`
			Value         struct {
				Type string `json:"type"`
				Key  string `json:"key"`
			} `json:"value"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		if body.IntegrationID != "openrouter" || body.Value.Type != "key" || body.Value.Key == "" {
			t.Errorf("unexpected credential payload: %+v", body)
		}
		json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
			"id": "cred_1", "integrationID": body.IntegrationID, "label": "l", "active": true,
		}})
	}))

	mux.HandleFunc("GET /api/event", requireAuth(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(": heartbeat\n\n"))
		w.Write([]byte(`data: {"type":"session.text.delta","data":{"sessionID":"ses_new","ordinal":0,"delta":"hi"}}` + "\n\n"))
	}))

	return httptest.NewServer(mux)
}

func TestClientCallsUseBasicAuth(t *testing.T) {
	srv := fakeServer(t, "pw")
	defer srv.Close()

	c := New(srv.URL, "pw")
	ctx := context.Background()

	info, err := c.Info(ctx)
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info.Version != "2.3.2" {
		t.Errorf("version = %q", info.Version)
	}

	s, err := c.CreateSession(ctx, "/data/projects/o/r", "a title")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if s.ID != "ses_new" || s.Location.Directory != "/data/projects/o/r" {
		t.Errorf("session = %+v", s)
	}

	sessions, err := c.ListSessions(ctx, "/data/projects/o/r", 5)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions) != 1 || sessions[0].ID != "ses_a" {
		t.Errorf("sessions = %+v", sessions)
	}

	if err := c.Prompt(ctx, s.ID, "do the thing"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}

	if _, err := c.SaveCredential(ctx, CredentialInput{
		IntegrationID: "openrouter", Label: "l", Key: "sk-x", Activate: true,
	}); err != nil {
		t.Fatalf("SaveCredential: %v", err)
	}

}

func TestClientRejectsBadPassword(t *testing.T) {
	srv := fakeServer(t, "right")
	defer srv.Close()

	_, err := New(srv.URL, "wrong").Info(context.Background())
	if err == nil {
		t.Fatal("expected an error for a wrong password")
	}
	apiErr, ok := err.(*APIError)
	if !ok || !apiErr.Unauthorized() {
		t.Fatalf("err = %v, want an unauthorized APIError", err)
	}
}

func TestDrainTranslatesStream(t *testing.T) {
	srv := fakeServer(t, "pw")
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var texts []string
	c := New(srv.URL, "pw")

	// The fake server closes the stream after one event, so Drain returns once
	// the body ends. Capture the translated text through the callback.
	emit := func(ev event.Event) { texts = append(texts, ev.Text) }
	if err := c.Drain(ctx, "ses_new", emit); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("Drain: %v", err)
	}
	if len(texts) != 1 || texts[0] != "hi" {
		t.Fatalf("texts = %v, want [hi]", texts)
	}
}
