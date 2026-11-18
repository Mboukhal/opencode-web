// Package ocjson parses `opencode run --format json` output lines into
// compact events for the UI.
package ocjson

import (
	"encoding/json"
	"strings"

	"opencodeweb/event"
)

// wireEvent mirrors the subset of the CLI's JSON lines we use.
type wireEvent struct {
	Type      string `json:"type"`
	SessionID string `json:"sessionID"`
	Part      struct {
		Type  string `json:"type"`
		Text  string `json:"text"`
		Tool  string `json:"tool"`
		State struct {
			Status string `json:"status"`
			Title  string `json:"title"`
			Input  struct {
				Command     string `json:"command"`
				FilePath    string `json:"filePath"`
				Pattern     string `json:"pattern"`
				Description string `json:"description"`
			} `json:"input"`
		} `json:"state"`
	} `json:"part"`
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// Line is the result of parsing one output line.
type Line struct {
	SessionID string
	Event     event.Event
}

// Parse returns the event encoded in line. ok is false for log noise and
// empty messages.
func Parse(line string) (Line, bool) {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "{") {
		return Line{}, false
	}
	var w wireEvent
	if err := json.Unmarshal([]byte(line), &w); err != nil {
		return Line{}, false
	}

	switch w.Type {
	case "text":
		if strings.TrimSpace(w.Part.Text) == "" {
			return Line{}, false
		}
		return Line{w.SessionID, event.Event{Type: "text", Text: w.Part.Text}}, true

	case "tool_use", "tool":
		in := w.Part.State.Input
		return Line{w.SessionID, event.Event{
			Type:   "tool",
			Tool:   w.Part.Tool,
			Status: w.Part.State.Status,
			Detail: detail(in.Command, in.FilePath, in.Pattern, in.Description, w.Part.State.Title),
		}}, true

	case "error":
		msg := w.Error.Message
		if msg == "" {
			msg = w.Error.Type
		}
		return Line{w.SessionID, event.Event{Type: "error", Error: msg}}, true
	}
	return Line{SessionID: w.SessionID}, false
}

func detail(vals ...string) string {
	for _, v := range vals {
		if v = strings.TrimSpace(v); v != "" {
			if len(v) > 160 {
				v = v[:160] + "…"
			}
			return v
		}
	}
	return ""
}
