package token

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

var ErrNotFound = errors.New("agentcell token not found")

type Store interface {
	Load() (string, error)
	Write(string) error
	// Delete removes a stored credential. It is idempotent: deleting a credential that is
	// already gone is success, not an error -- `agentcell logout` calls this unconditionally
	// once the server side is settled, and a logout that fails because it already happened
	// once is a CLI that cannot clean up after itself (the same argument auth_logout makes
	// server-side for its own idempotence).
	Delete() error
}

type FileStore struct{ Dir string }

func ConfigDir() (string, error) {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "agentcell"), nil
	}
	if runtime.GOOS == "windows" {
		dir, err := os.UserConfigDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(dir, "AgentCell"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "agentcell"), nil
}

func DefaultFileStore() (FileStore, error) { dir, err := ConfigDir(); return FileStore{Dir: dir}, err }
func (s FileStore) path() string           { return filepath.Join(s.Dir, "token") }

func (s FileStore) Load() (string, error) {
	b, err := os.ReadFile(s.path())
	if errors.Is(err, os.ErrNotExist) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("load AgentCell credential: %w", err)
	}
	token := strings.TrimSpace(string(b))
	if token == "" {
		return "", fmt.Errorf("load AgentCell credential: token file is empty")
	}
	return token, nil
}

func (s FileStore) Write(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return errors.New("refusing to store an empty AgentCell token")
	}
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return fmt.Errorf("prepare AgentCell credential directory: %w", err)
	}
	// Re-assert permissions on every write, including identical content.
	if err := os.Chmod(s.Dir, 0o700); err != nil {
		return fmt.Errorf("secure AgentCell credential directory: %w", err)
	}
	tmp, err := os.CreateTemp(s.Dir, ".token-")
	if err != nil {
		return fmt.Errorf("create AgentCell credential: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("secure AgentCell credential: %w", err)
	}
	if _, err := tmp.WriteString(value + "\n"); err != nil {
		tmp.Close()
		return fmt.Errorf("write AgentCell credential: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync AgentCell credential: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close AgentCell credential: %w", err)
	}
	if err := os.Rename(tmpName, s.path()); err != nil {
		return fmt.Errorf("replace AgentCell credential: %w", err)
	}
	if err := os.Chmod(s.path(), 0o600); err != nil {
		return fmt.Errorf("secure AgentCell credential: %w", err)
	}
	return nil
}

// Delete removes the stored token file. A file that is already gone is not an error: the
// caller's intent -- "this credential must not be usable from here" -- is already satisfied.
func (s FileStore) Delete() error {
	if err := os.Remove(s.path()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove AgentCell credential: %w", err)
	}
	return nil
}

// Resolver makes the environment override explicit and keeps storage swappable.
type Resolver struct {
	Environment func(string) string
	Store       Store
}

func (r Resolver) Load() (string, error) {
	getenv := r.Environment
	if getenv == nil {
		getenv = os.Getenv
	}
	if value := strings.TrimSpace(getenv("AGENTCELL_TOKEN")); value != "" {
		return value, nil
	}
	if r.Store == nil {
		return "", ErrNotFound
	}
	return r.Store.Load()
}
