package archive

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var serviceKeyFormat = regexp.MustCompile(`^[A-Za-z0-9:._-]{1,200}$`)

func TestScheduledKey(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	data, key, err := Directory(dir)
	if err != nil {
		t.Fatal(err)
	}
	if want := fmt.Sprintf("deploy-v1:%x", sha256.Sum256(data)); key != want || ScheduledKey(key, "") != want {
		t.Fatalf("the key without a schedule is not 0.1.2's: Directory=%q ScheduledKey=%q want %q", key, ScheduledKey(key, ""), want)
	}
	two, three := ScheduledKey(key, "0 2 * * *"), ScheduledKey(key, "0 3 * * *")
	if two == three || two == key {
		t.Fatalf("schedules not distinguished: %q %q %q", key, two, three)
	}
	if two != ScheduledKey(key, "0 2 * * *") {
		t.Fatal("ScheduledKey is not stable")
	}
	if want := key + ".s" + fmt.Sprintf("%x", sha256.Sum256([]byte("0 2 * * *")))[:16]; two != want {
		t.Fatalf("scheduled key = %q, want %q", two, want)
	}
	for _, k := range []string{key, two, three} {
		if !serviceKeyFormat.MatchString(k) {
			t.Fatalf("%q fails the service's key format", k)
		}
	}
}

// names lists the entry names Directory put in the archive, in archive order.
func names(t *testing.T, data []byte) []string {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	var out []string
	for {
		header, err := tr.Next()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, header.Name)
	}
}

func write(t *testing.T, root, rel, body string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestDirectorySkipsDependencyAndCacheDirectories: a frontend project's node_modules is routinely
// hundreds of MiB and tens of thousands of files, so sending it trips the service's archive limits
// (STATIC-CELLS.md §1) and the build installs its own anyway. The build tools' caches are the same
// shape. What the build PRODUCES -- dist, build, out -- is a site a user may have built locally and
// want served as-is, so it is kept.
func TestDirectorySkipsDependencyAndCacheDirectories(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "package.json", `{"scripts":{"build":"vite build"}}`)
	write(t, dir, "index.html", "<h1>hi</h1>")
	skipped := []string{
		"node_modules/react/index.js",
		"packages/web/node_modules/vite/bin.js",
		".next/cache/x",
		".svelte-kit/output/y",
		".turbo/log",
		".parcel-cache/z",
		".vite/deps/a.js",
		"src/.vite/deps/b.js",
		".git/HEAD",
	}
	for _, rel := range skipped {
		write(t, dir, rel, "skip")
	}
	kept := []string{
		"dist/index.html",
		"build/index.html",
		"out/index.html",
		"src/main.jsx",
		// A FILE named node_modules is not a dependency tree. Nothing installs one, so it is the
		// user's, and dropping it silently would be a surprise with no benefit.
		"docs/node_modules",
		"src/node_modules.md",
		".well-known/security.txt",
	}
	for _, rel := range kept {
		write(t, dir, rel, "keep")
	}
	data, _, err := Directory(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, name := range names(t, data) {
		got[name] = true
	}
	for _, rel := range skipped {
		for p := rel; p != "."; p = filepath.ToSlash(filepath.Dir(p)) {
			base := filepath.Base(p)
			if base == "node_modules" || base == ".next" || base == ".svelte-kit" || base == ".turbo" ||
				base == ".parcel-cache" || base == ".vite" || base == ".git" {
				if got[p] {
					t.Errorf("archive holds skipped directory %q", p)
				}
			}
		}
		if got[rel] {
			t.Errorf("archive holds %q, which is under a skipped directory", rel)
		}
	}
	for _, rel := range append(kept, "package.json", "index.html", "packages/web", "packages") {
		if !got[rel] {
			t.Errorf("archive lacks %q, which must be kept", rel)
		}
	}
}

// TestSkippedDirectoriesDoNotMoveTheKey: the idempotency key is the archive's digest, so a
// directory that is left out must not move it -- reinstalling dependencies is not a new deploy --
// while a change to a kept file must.
func TestSkippedDirectoriesDoNotMoveTheKey(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "index.html", "<h1>hi</h1>")
	write(t, dir, "dist/index.html", "built")
	write(t, dir, "sub/page.html", "sub") // so sub/ exists before its node_modules arrives
	_, before, err := Directory(dir)
	if err != nil {
		t.Fatal(err)
	}
	write(t, dir, "node_modules/left-pad/index.js", "module.exports = 1")
	write(t, dir, "sub/node_modules/x/y.js", "y")
	write(t, dir, ".vite/deps/z.js", "z")
	_, after, err := Directory(dir)
	if err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatalf("a skipped directory moved the key: %q -> %q", before, after)
	}
	write(t, dir, "dist/index.html", "rebuilt")
	_, changed, err := Directory(dir)
	if err != nil {
		t.Fatal(err)
	}
	if changed == after {
		t.Fatal("a change to dist/ did not move the key")
	}
}

// TestSymlinkStillRefused: the skip list is for real directories. A symlink -- even one named
// node_modules -- is refused as before, not silently dropped, so the refusal cannot be bypassed
// by naming.
func TestSymlinkStillRefused(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "index.html", "x")
	if err := os.Symlink(os.TempDir(), filepath.Join(dir, "node_modules")); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	if _, _, err := Directory(dir); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("a symlink named node_modules was not refused: %v", err)
	}
	// Positive control: the same tree without the symlink archives.
	if err := os.Remove(filepath.Join(dir, "node_modules")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Directory(dir); err != nil {
		t.Fatalf("the tree without the symlink was refused: %v", err)
	}
}
