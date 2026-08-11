//go:build archiveoverlay

package archive

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestArchiveIsIndependentOfParentCreationOrderAndMtime(t *testing.T) {
	first := filepath.Join(t.TempDir(), "tree-a")
	second := filepath.Join(t.TempDir(), "different-parent", "tree-b")
	for _, root := range []string{first, second} {
		if err := os.MkdirAll(filepath.Join(root, "nested"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(first, "a.txt"), []byte("alpha\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(first, "nested", "b.txt"), []byte("beta\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	// Reverse creation order in the other working tree.
	if err := os.WriteFile(filepath.Join(second, "nested", "b.txt"), []byte("beta\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(second, "a.txt"), []byte("alpha\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	old, future := time.Unix(123, 0), time.Unix(2_000_000_000, 0)
	for _, path := range []string{first, filepath.Join(first, "a.txt"), filepath.Join(first, "nested"), filepath.Join(first, "nested", "b.txt")} {
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{second, filepath.Join(second, "a.txt"), filepath.Join(second, "nested"), filepath.Join(second, "nested", "b.txt")} {
		if err := os.Chtimes(path, future, future); err != nil {
			t.Fatal(err)
		}
	}
	original, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(original)
	if err := os.Chdir(filepath.Dir(first)); err != nil {
		t.Fatal(err)
	}
	oneBytes, oneKey, err := Directory(filepath.Base(first))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(filepath.Dir(second)); err != nil {
		t.Fatal(err)
	}
	twoBytes, twoKey, err := Directory(filepath.Base(second))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(oneBytes, twoBytes) {
		t.Fatalf("archive bytes differ: %d versus %d", len(oneBytes), len(twoBytes))
	}
	if oneKey != twoKey {
		t.Fatalf("keys differ: %q versus %q", oneKey, twoKey)
	}
	if got := "deploy-v1:"; len(oneKey) != len(got)+64 || oneKey[:len(got)] != got {
		t.Fatalf("key shape = %q", oneKey)
	}

	// Negative control: this fixture must discriminate a conventional tar that preserves mtime.
	badOne := mtimeArchive(t, filepath.Join(first, "a.txt"))
	badTwo := mtimeArchive(t, filepath.Join(second, "a.txt"))
	if bytes.Equal(badOne, badTwo) {
		t.Fatal("negative control is blind to mtime")
	}
	t.Logf("byte-identical deterministic archives, %d bytes, key %s", len(oneBytes), oneKey)
}

func mtimeArchive(t *testing.T, path string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	gz := gzip.NewWriter(&buffer)
	tw := tar.NewWriter(gz)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	header, err := tar.FileInfoHeader(info, "")
	if err != nil {
		t.Fatal(err)
	}
	header.Name = "a.txt"
	if err := tw.WriteHeader(header); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}
