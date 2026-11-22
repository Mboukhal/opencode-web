package web

import (
	"net/http"
	"net/url"
	"strings"

	"opencodeweb/config"
)

// securityHeaders is the middleware that hardens every response:
//
//   - Content-Security-Policy: only same-origin scripts/styles/images, no
//     framing, no form posts off-site, no plugins. The app's own JS and CSS are
//     served from /static, and templates use inline event handlers sparingly.
//   - X-Frame-Options + frame-ancestors: the UI cannot be embedded, which is
//     the clickjacking guard for the buttons that run commands.
//   - X-Content-Type-Options: stop MIME sniffing of user-influenced responses.
//   - Referrer-Policy: never leak the pairing link (it is in the URL) onward.
//   - Cross-Origin-Opener-Policy / Resource-Policy: isolate the app.
//   - Permissions-Policy: disable sensors and APIs the UI never uses.
//   - Strict-Transport-Security: when served over TLS.
//   - Cache-Control on authenticated pages: never let a proxy keep them.
//
// It also refuses plain-HTTP requests when ForceHTTPS is set, unless the
// request came through a TLS-terminating proxy that says so.
func (s *Server) securityHeaders(next http.Handler) http.Handler {
	cfg := s.cfg
	csp := buildCSP(cfg)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.mustRedirectTLS(r) {
			target := "https://" + r.Host + r.URL.RequestURI()
			http.Redirect(w, r, target, http.StatusMovedPermanently)
			return
		}

		h := w.Header()
		h.Set("Content-Security-Policy", csp)
		h.Set("X-Frame-Options", "DENY")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		h.Set("Permissions-Policy",
			"camera=(), microphone=(), geolocation=(), payment=(), usb=(), "+
				"magnetometer=(), gyroscope=(), accelerometer=(), interest-cohort=()")
		h.Set("X-Permitted-Cross-Domain-Policies", "none")

		if isTLS(r) && cfg.HSTSMaxAge > 0 {
			h.Set("Strict-Transport-Security",
				"max-age="+itoa(cfg.HSTSMaxAge)+"; includeSubDomains; preload")
		}
		// Pages behind sign-in must not be cached by shared caches. Event
		// streams are excluded: they are already no-cache and long-lived.
		if r.URL.Path != "/healthz" && !strings.HasPrefix(r.URL.Path, "/static/") &&
			!strings.HasSuffix(r.URL.Path, "/events") {
			h.Set("Cache-Control", "no-store, private")
			h.Set("Pragma", "no-cache")
		}
		next.ServeHTTP(w, r)
	})
}

// buildCSP assembles the policy, allowing only what the UI actually renders.
func buildCSP(cfg *config.Config) string {
	// Inline handlers appear on a few inputs (select-on-click) and one form
	// confirmation, so inline script and style are permitted; everything else
	// is same-origin.
	script := "'self' 'unsafe-inline'"
	style := "'self' 'unsafe-inline'"
	img := "'self' data: https://lh3.googleusercontent.com https://avatars.githubusercontent.com"

	if extra := strings.TrimSpace(cfg.ExtraCSPOrigins); extra != "" {
		script += " " + extra
		style += " " + extra
		img += " " + extra
	}

	return strings.Join([]string{
		"default-src 'self'",
		"script-src " + script,
		"style-src " + style,
		"img-src " + img,
		"font-src 'self'",
		"connect-src 'self'",
		"form-action 'self'",
		"frame-ancestors 'none'",
		"base-uri 'self'",
		"object-src 'none'",
		"worker-src 'self'",
		"manifest-src 'self'",
	}, "; ")
}

// isTLS reports whether the client used HTTPS, directly or via a proxy.
func isTLS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// mustRedirectTLS decides whether a plain-HTTP request should be bounced.
func (s *Server) mustRedirectTLS(r *http.Request) bool {
	cfg := s.cfg
	if !cfg.ForceHTTPS || isTLS(r) {
		return false
	}
	// Health checks from the container runtime arrive over plain HTTP.
	if r.URL.Path == "/healthz" {
		return false
	}
	if r.Host == "" {
		return false
	}
	// Only redirect to a host we know about, never to an attacker-set Host.
	if u, err := url.Parse(cfg.PublicBaseURL); err == nil && u.Host != "" &&
		!strings.EqualFold(u.Host, r.Host) {
		return false
	}
	return true
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
