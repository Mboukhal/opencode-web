package web

import (
	"encoding/json"
	"net/url"
)

// eventJSON marshals one stream event for an SSE data frame.
func eventJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return `{"type":"error","error":"encode failed"}`
	}
	return string(b)
}

func urlEncode(s string) string { return url.QueryEscape(s) }
