package opencode

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"opencodeweb/event"
)

// wireEvent is the subset of the server's event envelope we decode.
type wireEvent struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

// eventPayload is the shared body of the session.* events.
type eventPayload struct {
	SessionID string `json:"sessionID"`
	// session.text.delta
	Delta   string `json:"delta"`
	Ordinal *int   `json:"ordinal"`
	// session.tool.called
	Input map[string]any `json:"input"`
	// Tool name lives inside the provider-native payload.
	Native struct {
		Name string `json:"name"`
	} `json:"native"`
	// session.step.failed / session.execution.failed
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
		Status  int    `json:"status"`
	} `json:"error"`
}

// Drain reads the /api/event stream until ctx is cancelled, converting each
// event that belongs to sessionID into a UI event and calling emit for it.
// Events for other sessions and unrelated event types are ignored.
func (c *Client) Drain(ctx context.Context, sessionID string, emit func(event.Event)) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/api/event", nil)
	if err != nil {
		return err
	}
	req.SetBasicAuth("opencode", c.pass)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("subscribing to events: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("subscribing to events: status %d", resp.StatusCode)
	}

	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		// SSE comment: the server sends ": heartbeat" to keep the stream alive.
		if line == "" || strings.HasPrefix(line, ":") || !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" {
			continue
		}
		if ev, ok := Translate([]byte(payload), sessionID); ok {
			emit(ev)
		}
	}
	if err := sc.Err(); err != nil && ctx.Err() == nil {
		return err
	}
	return ctx.Err()
}

// Translate converts one raw server event into a UI event for sessionID.
// ok is false when the event belongs to another session or carries nothing the
// UI shows. It is exported so it can be unit-tested without a live server.
//
// Text parts stream as deltas; session.text.ended repeats the whole part, so
// it is skipped to avoid duplicating what the deltas already delivered.
func Translate(raw []byte, sessionID string) (event.Event, bool) {
	var w wireEvent
	if err := json.Unmarshal(raw, &w); err != nil {
		return event.Event{}, false
	}
	var p eventPayload
	if len(w.Data) > 0 {
		if err := json.Unmarshal(w.Data, &p); err != nil {
			return event.Event{}, false
		}
	}
	if p.SessionID == "" || (sessionID != "" && p.SessionID != sessionID) {
		return event.Event{}, false
	}

	switch w.Type {
	case "session.text.delta":
		if p.Delta == "" {
			return event.Event{}, false
		}
		// Start a fresh block for every new text part.
		if p.Ordinal != nil && *p.Ordinal == 0 {
			return event.Event{Type: "text", Text: p.Delta, Status: "start"}, true
		}
		return event.Event{Type: "text", Text: p.Delta}, true

	case "session.text.ended":
		return event.Event{}, false

	case "session.tool.called":
		return event.Event{
			Type:   "tool",
			Tool:   orDefault(p.Native.Name, "tool"),
			Status: "running",
			Detail: toolDetail(p.Input),
		}, true

	case "session.tool.success":
		return event.Event{Type: "tool", Tool: orDefault(p.Native.Name, "tool"), Status: "completed"}, true

	case "session.tool.failed":
		return event.Event{Type: "tool", Tool: orDefault(p.Native.Name, "tool"), Status: "failed"}, true

	case "session.step.failed":
		// One step failed. The loop may retry, so report it but keep the run
		// open; session.execution.failed is what ends the turn.
		return event.Event{Type: "error", Error: stepMessage(p)}, true

	case "session.execution.failed":
		return event.Event{Type: "done", Error: stepMessage(p)}, true

	case "session.execution.succeeded":
		return event.Event{Type: "done"}, true

	case "session.execution.interrupted":
		return event.Event{Type: "done", Text: "stopped"}, true

	case "permission.asked":
		return event.Event{Type: "notice", Text: "OpenCode is waiting for a permission decision"}, true
	}
	return event.Event{}, false
}

// stepMessage is the human message for a failed step or turn.
func stepMessage(p eventPayload) string {
	if p.Error == nil {
		return "the model call failed"
	}
	if p.Error.Message != "" {
		return p.Error.Message
	}
	if p.Error.Type != "" {
		return p.Error.Type
	}
	return "the model call failed"
}

// toolDetail picks the most useful field out of a tool's free-form input.
func toolDetail(input map[string]any) string {
	for _, key := range []string{"command", "filePath", "path", "pattern", "description", "query"} {
		s, ok := input[key].(string)
		if !ok {
			continue
		}
		if s = strings.TrimSpace(s); s != "" {
			if len(s) > 160 {
				s = s[:160] + "…"
			}
			return s
		}
	}
	return ""
}

func orDefault(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}
