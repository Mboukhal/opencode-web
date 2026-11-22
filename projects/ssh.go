package projects

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// SSHKey describes the deploy key used for GitHub access.
type SSHKey struct {
	Path        string // private key file
	PublicKey   string // contents of <path>.pub when present
	Exists      bool
	Fingerprint string
}

// InspectKey reports whether the configured deploy key is usable.
func (m *Manager) InspectKey() SSHKey {
	k := SSHKey{Path: m.keyPath}
	if m.keyPath == "" {
		return k
	}
	if _, err := os.Stat(m.keyPath); err == nil {
		k.Exists = true
	}
	if b, err := os.ReadFile(m.keyPath + ".pub"); err == nil {
		k.PublicKey = strings.TrimSpace(string(b))
	}
	if k.Exists {
		if out, err := exec.Command("ssh-keygen", "-lf", m.keyPath+".pub").Output(); err == nil {
			fields := strings.Fields(string(out))
			if len(fields) >= 2 {
				k.Fingerprint = fields[1]
			}
		}
	}
	return k
}

// EnsureKey creates an ed25519 key pair at the configured path when missing.
// The private key is written 0600. It returns true when a key was created.
func (m *Manager) EnsureKey() (bool, error) {
	if m.keyPath == "" {
		return false, fmt.Errorf("no SSH_KEY_PATH configured")
	}
	if _, err := os.Stat(m.keyPath); err == nil {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(m.keyPath), 0o700); err != nil {
		return false, fmt.Errorf("creating ssh dir: %w", err)
	}
	// An empty passphrase keeps the key usable by non-interactive git.
	cmd := exec.Command("ssh-keygen", "-t", "ed25519", "-N", "", "-C", "opencode-web deploy key", "-f", m.keyPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		return false, fmt.Errorf("ssh-keygen: %s", strings.TrimSpace(string(out)))
	}
	if err := os.Chmod(m.keyPath, 0o600); err != nil {
		return false, err
	}
	if err := m.knownHosts(); err != nil {
		m.log().Warn("scanning github host key", "err", err)
	}
	return true, nil
}

// knownHosts records GitHub's host keys so the first clone is not rejected.
func (m *Manager) knownHosts() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	dir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	path := filepath.Join(dir, "known_hosts")
	if b, err := os.ReadFile(path); err == nil && strings.Contains(string(b), "github.com") {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ssh-keyscan", "-t", "ed25519,rsa", "github.com")
	out, err := cmd.Output()
	if err != nil || len(out) == 0 {
		return fmt.Errorf("ssh-keyscan github.com failed: %v", err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(out)
	return err
}

// TestKey runs `ssh -T git@github.com`, which GitHub answers with a greeting
// even on success (exit code 1), so the greeting text is parsed instead.
func (m *Manager) TestKey(ctx context.Context) (string, error) {
	args := []string{"-T", "-o", "StrictHostKeyChecking=accept-new", "-o", "BatchMode=yes"}
	if m.keyPath != "" {
		args = append(args, "-i", m.keyPath, "-o", "IdentitiesOnly=yes")
	}
	args = append(args, "git@github.com")
	out, err := exec.CommandContext(ctx, "ssh", args...).CombinedOutput()
	msg := strings.TrimSpace(string(out))
	if strings.Contains(msg, "successfully authenticated") {
		return msg, nil
	}
	if err != nil {
		return msg, fmt.Errorf("github auth failed: %s", msg)
	}
	return msg, nil
}
