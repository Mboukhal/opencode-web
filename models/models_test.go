package models

import "testing"

func TestParse(t *testing.T) {
	tests := []struct {
		name      string
		spec      string
		primary   string
		fallbacks []string
	}{
		{"single", "openrouter/deepseek-v4", "openrouter/deepseek-v4", nil},
		{"chain", "a/b, c/d ,e/f", "a/b", []string{"c/d", "e/f"}},
		{"empty", "  ", "", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := Parse(tt.spec)
			if c.Primary != tt.primary {
				t.Fatalf("primary = %q, want %q", c.Primary, tt.primary)
			}
			if len(c.Fallbacks) != len(tt.fallbacks) {
				t.Fatalf("fallbacks = %v, want %v", c.Fallbacks, tt.fallbacks)
			}
		})
	}
}

func TestIsExhaustion(t *testing.T) {
	tests := []struct {
		msg  string
		want bool
	}{
		{"Insufficient credits", true},
		{"429 Too Many Requests", true},
		{"rate limit exceeded", true},
		{"Payment required", true},
		{"Model unavailable: opencode/foo", false},
		{"context length exceeded the model limit", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := IsExhaustion(tt.msg); got != tt.want {
			t.Errorf("IsExhaustion(%q) = %v, want %v", tt.msg, got, tt.want)
		}
	}
}

func TestIsExhaustionAuth(t *testing.T) {
	for _, msg := range []string{"User not found.", "Invalid API key", "401 Unauthorized"} {
		if !IsExhaustion(msg) {
			t.Errorf("IsExhaustion(%q) = false, want true", msg)
		}
	}
	if IsExhaustion("Model unavailable: opencode/foo") {
		t.Error("model-unavailable must not trigger a fallback")
	}
}
