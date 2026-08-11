package token

import (
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
