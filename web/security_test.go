package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSecurityHeadersPresent(t *testing.T) {
	srv := newTestServer(t)
	h := srv.Handler()

	for _, path := range []string{"/", "/healthz", "/static/app.css"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		checks := map[string]string{
			"X-Content-Type-Options": "nosniff",
			"X-Frame-Options":        "DENY",
			"Referrer-Policy":        "same-origin",
		}
		for header, want := range checks {
			if got := rec.Header().Get(header); got != want {
				t.Errorf("%s: %s = %q, want %q", path, header, got, want)
			}
		}

		csp := rec.Header().Get("Content-Security-Policy")
		for _, want := range []string{
			"default-src 'self'", "frame-ancestors 'none'", "object-src 'none'",
			"form-action 'self'", "base-uri 'self'",
		} {
			if !strings.Contains(csp, want) {
				t.Errorf("%s: CSP missing %q (got %q)", path, want, csp)
			}
		}
		if pp := rec.Header().Get("Permissions-Policy"); !strings.Contains(pp, "camera=()") {
			t.Errorf("%s: Permissions-Policy = %q", path, pp)
		}
	}
}

func TestAuthPagesAreNotCached(t *testing.T) {
	srv := newTestServer(t)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "no-store") {
		t.Errorf("Cache-Control = %q, want no-store", cc)
	}
}

func TestHSTSOnlyOverTLS(t *testing.T) {
	srv := newTestServer(t)
	srv.cfg.HSTSMaxAge = 3600

	// Plain HTTP, no proxy hint: no HSTS.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	srv.securityHeaders(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})).
		ServeHTTP(rec, req)
	if got := rec.Header().Get("Strict-Transport-Security"); got != "" {
		t.Errorf("HSTS sent over plain HTTP: %q", got)
	}

	// Behind a TLS-terminating proxy.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	srv.securityHeaders(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})).
		ServeHTTP(rec, req)
	hsts := rec.Header().Get("Strict-Transport-Security")
	if !strings.Contains(hsts, "max-age=3600") || !strings.Contains(hsts, "includeSubDomains") {
		t.Errorf("HSTS = %q", hsts)
	}
}

func TestForceHTTPSRedirect(t *testing.T) {
	srv := newTestServer(t)
	srv.cfg.ForceHTTPS = true
	srv.cfg.PublicBaseURL = "https://code.example.dev"

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://code.example.dev/projects?x=1", nil)
	srv.securityHeaders(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})).
		ServeHTTP(rec, req)

	if rec.Code != http.StatusMovedPermanently {
		t.Fatalf("code = %d, want 301", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "https://code.example.dev/projects?x=1" {
		t.Errorf("Location = %q", got)
	}

	// A proxy that already terminated TLS must not be redirected (no loop).
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "http://code.example.dev/projects", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	srv.securityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("code behind proxy = %d, want 200", rec.Code)
	}

	// An unexpected Host is never used as a redirect target.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "http://evil.example/", nil)
	srv.securityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("code for foreign Host = %d, want 200", rec.Code)
	}
}

func TestExtraCSPOrigins(t *testing.T) {
	srv := newTestServer(t)
	srv.cfg.ExtraCSPOrigins = "https://fonts.example.com"
	csp := buildCSP(srv.cfg)
	if !strings.Contains(csp, "https://fonts.example.com") {
		t.Errorf("extra origin missing from CSP: %q", csp)
	}
}
