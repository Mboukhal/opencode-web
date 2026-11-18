// Package auth implements Google sign-in and cookie-backed sessions.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"

	"opencodeweb/config"
	"opencodeweb/store"
)

const sessionCookie = "ow_session"

// ErrNotAllowed is returned when an authenticated Google account is not on the allow-list.
var ErrNotAllowed = errors.New("auth: email not allowed")

// Manager owns OAuth configuration, the allow-list and session cookies.
type Manager struct {
	oauth    *oauth2.Config
	store    *store.Store
	allowed  []string
	baseURL  string
	secure   bool
	sessions time.Duration
}

// New builds an auth Manager.
func New(cfg *config.Config, st *store.Store) *Manager {
	return &Manager{
		oauth: &oauth2.Config{
			ClientID:     cfg.GoogleClientID,
			ClientSecret: cfg.GoogleClientSecret,
			RedirectURL:  cfg.RedirectURL(),
			Endpoint:     google.Endpoint,
			Scopes:       []string{"openid", "email", "profile"},
		},
		store:    st,
		allowed:  cfg.AllowedEmails,
		baseURL:  cfg.PublicBaseURL,
		secure:   strings.HasPrefix(cfg.PublicBaseURL, "https://"),
		sessions: 30 * 24 * time.Hour,
	}
}

// StartAuth creates a state value and redirects to Google.
func (m *Manager) StartAuth(w http.ResponseWriter, r *http.Request) {
	state, err := randomToken(32)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if err := m.store.PutOAuthState(r.Context(), state); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     "ow_oauth_state",
		Value:    state,
		Path:     "/",
		HttpOnly: true,
		Secure:   m.secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   600,
	})
	http.Redirect(w, r, m.oauth.AuthCodeURL(state, oauth2.AccessTypeOnline), http.StatusFound)
}

// FinishAuth validates the callback and establishes a session cookie.
func (m *Manager) FinishAuth(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if e := q.Get("error"); e != "" {
		http.Error(w, "sign-in cancelled", http.StatusUnauthorized)
		return
	}

	cookie, err := r.Cookie("ow_oauth_state")
	if err != nil || cookie.Value == "" || cookie.Value != q.Get("state") {
		http.Error(w, "invalid oauth state", http.StatusBadRequest)
		return
	}
	ok, err := m.store.ConsumeOAuthState(r.Context(), q.Get("state"))
	if err != nil || !ok {
		http.Error(w, "invalid oauth state", http.StatusBadRequest)
		return
	}

	tok, err := m.oauth.Exchange(r.Context(), q.Get("code"))
	if err != nil {
		http.Error(w, "code exchange failed", http.StatusBadRequest)
		return
	}

	info, err := fetchUserInfo(r.Context(), m.oauth, tok)
	if err != nil {
		http.Error(w, "could not read Google profile", http.StatusBadGateway)
		return
	}
	email := strings.ToLower(info.Email)
	if !info.EmailVerified || !m.IsAllowed(email) {
		http.Error(w, fmt.Sprintf("%s is not allowed to sign in", email), http.StatusForbidden)
		return
	}

	user, err := m.store.UpsertUser(r.Context(), email, info.Name, info.Picture)
	if err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}

	token, err := randomToken(32)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if err := m.store.CreateSession(r.Context(), hashToken(token), user.ID, m.sessions, r.UserAgent()); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   m.secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(m.sessions.Seconds()),
	})
	http.Redirect(w, r, "/projects", http.StatusFound)
}

// Logout revokes the session and clears the cookie.
func (m *Manager) Logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		m.store.DeleteSession(r.Context(), hashToken(c.Value))
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: m.secure, SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, "/", http.StatusFound)
}

// IsAllowed reports whether email is on the allow-list.
func (m *Manager) IsAllowed(email string) bool {
	return slices.Contains(m.allowed, strings.ToLower(email))
}

// CurrentUser resolves the session cookie to a user, or nil.
func (m *Manager) CurrentUser(r *http.Request) (*store.User, error) {
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return nil, nil
	}
	return m.store.UserForSession(r.Context(), hashToken(c.Value))
}

type userInfo struct {
	Email         string `json:"email"`
	EmailVerified bool   `json:"email_verified"`
	Name          string `json:"name"`
	Picture       string `json:"picture"`
}

func fetchUserInfo(ctx context.Context, cfg *oauth2.Config, tok *oauth2.Token) (*userInfo, error) {
	client := cfg.Client(ctx, tok)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://openidconnect.googleapis.com/v1/userinfo", nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("userinfo status %d", resp.StatusCode)
	}
	var info userInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, fmt.Errorf("decoding userinfo: %w", err)
	}
	return &info, nil
}

func randomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func hashToken(tok string) string {
	sum := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(sum[:])
}
