// Package service supervises the OpenCode background server on this machine.
//
// The project talks to OpenCode through its shared server rather than spawning
// a private `--standalone` server per run: that shared server is the one the
// OpenCode apps connect to, so sessions started from the web UI are the same
// sessions OpenCode Mobile (and the terminal and desktop apps) can open.
package service

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"opencodeweb/opencode"
)

// Settings configure how the server is started.
type Settings struct {
	Bin       string // opencode CLI
	ConfigDir string // XDG_CONFIG_HOME
	StateDir  string // XDG_DATA_HOME / XDG_STATE_HOME
	Home      string // HOME
	Hostname  string // "0.0.0.0" lets phones on the LAN reach it directly
	Port      int    // 0 lets OpenCode pick a free port
	Password  string // service password; generated when empty
	Env       map[string]string
	Log       *slog.Logger
}

// Manager owns the running server process and the client bound to it.
type Manager struct {
	set Settings

	mu     sync.Mutex
	cmd    *exec.Cmd
	addr   string
	pass   string
	client *opencode.Client
}

// New creates a Manager. Call Start to launch the server.
func New(s Settings) *Manager { return &Manager{set: s} }

// Start launches the server if it is not already answering, then returns a
// client bound to it. It is safe to call more than once.
func (m *Manager) Start(ctx context.Context) (*opencode.Client, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.client != nil {
		return m.client, nil
	}

	pass := m.set.Password
	if pass == "" {
		// Reuse the password from the previous boot so phones that already
		// paired keep working; generate one only on first start.
		pass = m.loadPassword()
	}
	if pass == "" {
		var err error
		if pass, err = randomPassword(); err != nil {
			return nil, err
		}
	}
	// Persist the password so a restart keeps paired apps working; the file is
	// 0600 and only the OpenCode user can read it.
	if err := m.persistPassword(pass); err != nil {
		m.log().Warn("could not persist service password", "err", err)
	}

	addr, err := m.startProcess(ctx, pass)
	if err != nil {
		return nil, err
	}
	m.addr, m.pass = addr, pass
	m.client = opencode.New("http://"+addr, pass)
	// Record the port so a restart can reattach to the same server address.
	if err := m.persistPassword(pass); err != nil {
		m.log().Warn("could not record server address", "err", err)
	}
	return m.client, nil
}

// Client returns the bound client, or nil before Start succeeds.
func (m *Manager) Client() *opencode.Client {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.client
}

// Address is the host:port the server listens on.
func (m *Manager) Address() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.addr
}

// Stop terminates a server this Manager started. A pre-existing shared server
// that we merely connected to is left running.
func (m *Manager) Stop() {
	m.mu.Lock()
	cmd := m.cmd
	m.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Signal(os.Interrupt)
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
	}
}

// startProcess reuses a healthy shared server, otherwise starts `opencode serve`.
func (m *Manager) startProcess(ctx context.Context, pass string) (string, error) {
	if addr := m.existingAddress(ctx, pass); addr != "" {
		m.log().Info("using running opencode server", "addr", addr)
		return addr, nil
	}

	args := []string{"serve", "--hostname", hostOrDefault(m.set.Hostname)}
	if m.set.Port > 0 {
		args = append(args, "--port", fmt.Sprint(m.set.Port))
	}

	cmd := exec.Command(m.set.Bin, args...)
	cmd.Dir = m.set.Home
	cmd.Env = m.childEnv(pass)

	out, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	// The server logs to stderr; forward it so failures are diagnosable.
	cmd.Stderr = &logWriter{log: m.log(), prefix: "opencode"}
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("starting opencode serve: %w", err)
	}
	m.cmd = cmd

	// The first stdout line is "server listening on http://host:port"; the
	// second is the password. Read the address, then wait for health.
	addrCh := make(chan string, 1)
	go func() {
		buf := make([]byte, 512)
		n, _ := out.Read(buf)
		addrCh <- parseAddress(string(buf[:n]))
	}()

	var addr string
	select {
	case addr = <-addrCh:
	case <-time.After(30 * time.Second):
		m.Stop()
		return "", errors.New("opencode serve did not announce an address")
	}
	if addr == "" {
		m.Stop()
		return "", errors.New("could not parse the address from opencode serve output")
	}

	client := opencode.New("http://"+addr, pass)
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		probe, cancel := context.WithTimeout(ctx, 2*time.Second)
		_, err := client.Info(probe)
		cancel()
		if err == nil {
			m.log().Info("opencode server ready", "addr", addr)
			return addr, nil
		}
		time.Sleep(300 * time.Millisecond)
	}
	m.Stop()
	return "", fmt.Errorf("opencode server at %s never became healthy", addr)
}

// existingAddress probes the state file and default port for a server that is
// already up with the password we are going to use.
func (m *Manager) existingAddress(ctx context.Context, pass string) string {
	for _, cand := range []string{
		fmt.Sprintf("127.0.0.1:%d", m.set.Port),
		m.stateAddress(),
	} {
		if cand == "" || strings.HasSuffix(cand, ":0") {
			continue
		}
		probe, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
		_, err := opencode.New("http://"+cand, pass).Info(probe)
		cancel()
		if err == nil {
			return cand
		}
	}
	return ""
}

// stateAddress returns the host:port recorded by a previous start.
func (m *Manager) stateAddress() string {
	b, err := os.ReadFile(filepath.Join(m.set.StateDir, "opencode-web", "server.addr"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// loadPassword returns the password saved on a previous boot, or "".
func (m *Manager) loadPassword() string {
	b, err := os.ReadFile(filepath.Join(m.set.StateDir, "opencode-web", "server.password"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// persistPassword writes the password and the announced address so restarts
// and the pairing page can reuse them.
func (m *Manager) persistPassword(pass string) error {
	dir := filepath.Join(m.set.StateDir, "opencode-web")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "server.password"), []byte(pass), 0o600); err != nil {
		return err
	}
	if m.addr != "" {
		return os.WriteFile(filepath.Join(dir, "server.addr"), []byte(m.addr), 0o600)
	}
	return nil
}

// SaveAddress records the address of the running server for the next start.
func (m *Manager) SaveAddress() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.addr == "" {
		return
	}
	dir := filepath.Join(m.set.StateDir, "opencode-web")
	os.MkdirAll(dir, 0o700)
	os.WriteFile(filepath.Join(dir, "server.addr"), []byte(m.addr), 0o600)
}

func (m *Manager) childEnv(pass string) []string {
	env := []string{
		"HOME=" + m.set.Home,
		"XDG_CONFIG_HOME=" + m.set.ConfigDir,
		"XDG_DATA_HOME=" + m.set.StateDir,
		"XDG_STATE_HOME=" + m.set.StateDir,
		"OPENCODE_CONFIG_DIR=" + m.set.ConfigDir,
		"OPENCODE_SERVER_PASSWORD=" + pass,
		"PATH=" + os.Getenv("PATH"),
	}
	for k, v := range m.set.Env {
		env = append(env, k+"="+v)
	}
	return env
}

func (m *Manager) log() *slog.Logger {
	if m.set.Log != nil {
		return m.set.Log
	}
	return slog.Default()
}

// parseAddress pulls host:port out of "server listening on http://127.0.0.1:4096".
func parseAddress(s string) string {
	i := strings.Index(s, "http://")
	if i < 0 {
		return ""
	}
	rest := s[i+len("http://"):]
	if j := strings.IndexAny(rest, " \r\n"); j >= 0 {
		rest = rest[:j]
	}
	if _, _, err := net.SplitHostPort(rest); err != nil {
		return ""
	}
	return rest
}

func hostOrDefault(h string) string {
	if strings.TrimSpace(h) == "" {
		return "127.0.0.1"
	}
	return h
}

func randomPassword() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generating service password: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// logWriter streams server stderr into app logs line by line.
type logWriter struct {
	log    *slog.Logger
	prefix string
	mu     sync.Mutex
}

func (w *logWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, line := range strings.Split(strings.TrimRight(string(p), "\n"), "\n") {
		if strings.TrimSpace(line) != "" {
			w.log.Info(w.prefix, "line", line)
		}
	}
	return len(p), nil
}
