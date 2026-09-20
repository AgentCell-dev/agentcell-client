package token

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestWriteReassertsPermissionsForIdenticalContent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not expose Unix permission bits")
	}
	store := FileStore{Dir: t.TempDir()}
	if err := store.Write("secret-token"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(store.Dir, "token")
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(store.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := store.Write("secret-token"); err != nil {
		t.Fatal(err)
	}
	fileInfo, _ := os.Stat(path)
	if got := fileInfo.Mode().Perm(); got != 0o600 {
		t.Fatalf("file mode = %o, want 600", got)
	}
	dirInfo, _ := os.Stat(store.Dir)
	if got := dirInfo.Mode().Perm(); got != 0o700 {
		t.Fatalf("directory mode = %o, want 700", got)
	}
}

func TestEnvironmentPrecedesFile(t *testing.T) {
	store := FileStore{Dir: t.TempDir()}
	if err := store.Write("file-token"); err != nil {
		t.Fatal(err)
	}
	resolver := Resolver{Environment: func(string) string { return "environment-token" }, Store: store}
	got, err := resolver.Load()
	if err != nil || got != "environment-token" {
		t.Fatalf("got %q, %v", got, err)
	}
}

// TestDeleteRemovesTheTokenAndIsIdempotent: `agentcell logout` calls Delete unconditionally, even
// when the server side already dropped the credential (a dead token still clears locally). The
// second call, on a file that is already gone, must not be an error.
func TestDeleteRemovesTheTokenAndIsIdempotent(t *testing.T) {
	store := FileStore{Dir: t.TempDir()}
	if err := store.Write("file-token"); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(); err != nil {
		t.Fatalf("first delete: %v", err)
	}
	if _, err := store.Load(); !errors.Is(err, ErrNotFound) {
		t.Fatalf("token still loadable after delete: %v", err)
	}
	if err := store.Delete(); err != nil {
		t.Fatalf("second delete on an already-gone file must not error: %v", err)
	}
}
