// Package models resolves the model chain used for runs: a primary model plus
// ordered fallbacks, so a run continues on a free model when the paid one runs
// out of credit or is rate limited.
package models

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Chain is an ordered list of provider/model ids.
type Chain struct {
	Primary   string
	Fallbacks []string
}

// Parse builds a Chain from a comma-separated list. The first entry is primary.
func Parse(spec string) Chain {
	var ids []string
	for _, p := range strings.Split(spec, ",") {
		if p = strings.TrimSpace(p); p != "" {
			ids = append(ids, p)
		}
	}
	if len(ids) == 0 {
		return Chain{}
	}
	return Chain{Primary: ids[0], Fallbacks: ids[1:]}
}

// All returns primary followed by fallbacks.
func (c Chain) All() []string {
	if c.Primary == "" {
		return nil
	}
	return append([]string{c.Primary}, c.Fallbacks...)
}

// IsExhaustion reports whether an error means "no credit / quota / rate limit",
// which is the only case where falling back to a free model is appropriate.
func IsExhaustion(msg string) bool {
	m := strings.ToLower(msg)
	// A context-length error is not a billing problem; retrying on another
	// model with the same history would fail the same way.
	if strings.Contains(m, "context length") || strings.Contains(m, "context_length") {
		return false
	}
	for _, needle := range []string{
		"insufficient", "credit", "quota", "rate limit", "rate-limit", "429",
		"402", "payment", "billing", "exceeded", "no tokens", "out of tokens",
		"usage limit", "too many requests",
		// A key that is invalid, revoked or out of credit also reports as an
		// auth failure; falling back to a free model keeps runs working.
		"user not found", "invalid api key", "invalid_api_key", "unauthorized",
		"authentication", "no auth credentials",
	} {
		if strings.Contains(m, needle) {
			return true
		}
	}
	return false
}

// FreeModel asks OpenRouter for the newest free model that supports tools.
// It returns "" when the lookup fails, so callers keep their static fallback.
func FreeModel(ctx context.Context, apiKey string) string {
	if apiKey == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://openrouter.ai/api/v1/models", nil)
	if err != nil {
		return ""
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}

	var payload struct {
		Data []struct {
			ID            string `json:"id"`
			Created       int64  `json:"created"`
			ContextLength int    `json:"context_length"`
			Pricing       struct {
				Prompt     string `json:"prompt"`
				Completion string `json:"completion"`
			} `json:"pricing"`
			SupportedParameters []string `json:"supported_parameters"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return ""
	}

	best := ""
	var bestCreated int64
	for _, m := range payload.Data {
		if m.Pricing.Prompt != "0" || m.Pricing.Completion != "0" {
			continue
		}
		if !strings.HasSuffix(m.ID, ":free") {
			continue
		}
		if !supportsTools(m.SupportedParameters) {
			continue
		}
		if m.ContextLength < 32000 {
			continue
		}
		if m.Created > bestCreated {
			best, bestCreated = m.ID, m.Created
		}
	}
	if best == "" {
		return ""
	}
	return "openrouter/" + best
}

func supportsTools(params []string) bool {
	for _, p := range params {
		if p == "tools" {
			return true
		}
	}
	return false
}

// Describe renders a chain for logs.
func (c Chain) Describe() string {
	if c.Primary == "" {
		return "(none)"
	}
	return fmt.Sprintf("%s → %s", c.Primary, strings.Join(c.Fallbacks, " → "))
}
