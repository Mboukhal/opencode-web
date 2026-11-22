package web

import (
	"context"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strings"
	"time"

	"opencodeweb/store"
)

// mobilePage is the pairing view for OpenCode Mobile.
type mobilePage struct {
	// PairURL is the one-time link a phone opens (or scans as a QR code).
	PairURL string
	// QR is the pairing link rendered as inline SVG.
	QR template.HTML
	// ExpiresIn is the lifetime of the code in seconds.
	ExpiresIn int
	// ServerAddr is the OpenCode server address on this host.
	ServerAddr string
	AppURL     string
	// PairError explains why no link could be produced.
	PairError string
	// PairWarning explains a misconfiguration that makes pairing fail even
	// though a link was produced.
	PairWarning string
	// Ready is false until the OpenCode server answered.
	Ready bool
}

// handleMobile shows the mobile pairing page. Pairing codes are single use and
// short lived, so a link is only minted when the user asks for one.
func (s *Server) handleMobile(w http.ResponseWriter, r *http.Request, u *store.User) {
	page := mobilePage{
		AppURL:     "https://opencode.ai/mobile",
		ServerAddr: s.svc.Address(),
	}
	if s.cfg.PairOriginIsApp() {
		page.PairWarning = "Phones cannot pair through " + s.cfg.PairOrigin() +
			": that address serves this web app, not the OpenCode server. Set " +
			"MOBILE_PAIR_URL to a hostname routed to the OpenCode server (e.g. https://opencode.mboukhal.dev)."
	}
	if client := s.client(); client != nil {
		page.Ready = true
		if v, _ := s.store.GetSetting(r.Context(), "mobile_pair_url"); v != "" {
			page.PairURL = v
			page.QR = template.HTML(qrHTML(v))
			page.ExpiresIn = 300
		}
	} else {
		page.PairError = "The OpenCode server is still starting."
	}
	s.render(w, r, "mobile", "Mobile", page)
}

// handlePair mints a fresh one-time pairing link and shows it on the page.
func (s *Server) handlePair(w http.ResponseWriter, r *http.Request, u *store.User) {
	client := s.client()
	if client == nil {
		http.Error(w, "the OpenCode server is not running yet", http.StatusServiceUnavailable)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	code, err := client.CreatePairing(ctx)
	if err != nil {
		s.log.Error("creating pairing code", "err", err)
		http.Error(w, "could not create a pairing code: "+err.Error(), http.StatusBadGateway)
		return
	}

	// The phone must reach the OpenCode server itself, so the link points at
	// the server's public origin, not the web app's API path.
	link := fmt.Sprintf("%s/auth/connect/%s", s.cfg.PairOrigin(), url.PathEscape(code.Code))
	s.store.SetSetting(ctx, "mobile_pair_url", link)
	s.store.SetSetting(ctx, "mobile_pair_at", fmt.Sprint(time.Now().Unix()))

	http.Redirect(w, r, "/mobile", http.StatusFound)
}

// settingsPage reports whether the shared pieces (server, skills, keys, git)
// are wired up, so problems are visible without reading logs.
type settingsPage struct {
	ServerAddr   string
	ServerOK     bool
	ServerErr    string
	Model        string
	SkillsSrc    string
	SkillsDir    string
	Skills       []string
	SkillErr     string
	Providers    []string
	KeyCount     int
	KeyErr       string
	SSHKey       SSHKeyView
	GitHubAuth   string
	GitHubAuthOK bool
}

// SSHKeyView is the deploy-key summary shown in settings.
type SSHKeyView struct {
	Path        string
	Exists      bool
	Fingerprint string
	PublicKey   string
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request, u *store.User) {
	page := settingsPage{
		Model:     s.cfg.OpenCodeModel,
		SkillsSrc: s.cfg.SkillsSrc,
		SkillsDir: s.ocfg.SkillsDir(),
	}
	if s.svc != nil {
		page.ServerAddr = s.svc.Address()
	}
	for id := range s.cfg.ProviderKeys {
		page.Providers = append(page.Providers, id)
	}

	if client := s.client(); client != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		if info, err := client.Info(ctx); err != nil {
			page.ServerErr = err.Error()
		} else {
			page.ServerOK = true
			page.ServerAddr = fmt.Sprintf("%s (v%s)", info.URLs, info.Version)
		}
		cancel()

		ctx, cancel = context.WithTimeout(r.Context(), 15*time.Second)
		if skills, err := client.Skills(ctx, ""); err != nil {
			page.SkillErr = err.Error()
		} else {
			for _, sk := range skills {
				page.Skills = append(page.Skills, sk.Name)
			}
		}
		cancel()

		ctx, cancel = context.WithTimeout(r.Context(), 15*time.Second)
		if creds, err := client.Credentials(ctx); err != nil {
			page.KeyErr = err.Error()
		} else {
			page.KeyCount = len(creds)
		}
		cancel()
	} else {
		page.ServerErr = "not started"
	}

	key := s.projects.InspectKey()
	page.SSHKey = SSHKeyView{Path: key.Path, Exists: key.Exists, Fingerprint: key.Fingerprint, PublicKey: key.PublicKey}

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	if msg, err := s.projects.TestKey(ctx); err == nil {
		page.GitHubAuth = strings.TrimSpace(msg)
		page.GitHubAuthOK = true
	} else {
		page.GitHubAuth = strings.TrimSpace(msg)
	}
	s.render(w, r, "settings", "Settings", page)
}
