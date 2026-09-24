package archive

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// skippedDirectories are left out of the archive wherever they appear, and only when they are real
// directories: a file with one of these names is the user's and is sent, and a symlink with one is
// refused below like any other symlink, so naming cannot slip one past that refusal. node_modules
// is a dependency tree the build reinstalls from package.json and the lockfile; for a frontend
// project it is routinely hundreds of MiB and tens of thousands of files, which trips the service's
// archive limits (STATIC-CELLS.md §1). The dot-directories are the frontend tools' caches (Next.js,
// SvelteKit, Turborepo, Parcel, Vite). What a build PRODUCES -- dist, build, out -- is deliberately
// absent: a directory holding an already-built site is served as one, so it must arrive.
var skippedDirectories = map[string]bool{
	"node_modules":  true,
	".next":         true,
	".svelte-kit":   true,
	".turbo":        true,
	".parcel-cache": true,
	".vite":         true,
}

// Directory creates deterministic tar+gzip bytes: stable order, timestamps,
// uid/gid and gzip metadata. The digest is therefore a stable retry key.
func Directory(root string) ([]byte, string, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, "", err
	}
	var paths []string
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if rel == ".git" || strings.HasPrefix(rel, ".git"+string(filepath.Separator)) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if rel != "." && entry.IsDir() && skippedDirectories[entry.Name()] {
			return filepath.SkipDir
		}
		if rel != "." {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	sort.Strings(paths)
	var buffer bytes.Buffer
	gz, _ := gzip.NewWriterLevel(&buffer, gzip.BestCompression)
	gz.Header.ModTime = time.Unix(0, 0)
	gz.Header.OS = 255
	tw := tar.NewWriter(gz)
	for _, path := range paths {
		info, err := os.Lstat(path)
		if err != nil {
			return nil, "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, "", fmt.Errorf("symlink %s is not supported", path)
		}
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return nil, "", err
		}
		header.Name, _ = filepath.Rel(root, path)
		header.Name = filepath.ToSlash(header.Name)
		header.ModTime, header.AccessTime, header.ChangeTime = time.Unix(0, 0), time.Time{}, time.Time{}
		header.Uid, header.Gid, header.Uname, header.Gname = 0, 0, "", ""
		if err := tw.WriteHeader(header); err != nil {
			return nil, "", err
		}
		if info.Mode().IsRegular() {
			file, err := os.Open(path)
			if err != nil {
				return nil, "", err
			}
			_, copyErr := io.Copy(tw, file)
			closeErr := file.Close()
			if copyErr != nil {
				return nil, "", copyErr
			}
			if closeErr != nil {
				return nil, "", closeErr
			}
		}
	}
	if err := tw.Close(); err != nil {
		return nil, "", err
	}
	if err := gz.Close(); err != nil {
		return nil, "", err
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(buffer.Bytes()))
	return buffer.Bytes(), "deploy-v1:" + digest, nil
}

// ScheduledKey is the deploy idempotency key once the request's schedule is known
// (SCHEDULED-CELLS.md §2.2). The service replays a key it has seen as `unchanged`, so a key that
// is a digest of the archive alone would replay `--schedule "0 3 * * *"` as the earlier
// `--schedule "0 2 * * *"` of the same directory and the schedule would never change. With no
// schedule the key is returned untouched -- byte-identical to 0.1.2's, so no web deploy stops
// replaying across the upgrade -- and with one it gains `.s` and the first 16 hex of
// sha256(schedule): still inside the service's `^[A-Za-z0-9:._-]{1,200}$`, and still leaving the
// archive digest first after the colon, where the service takes a default cell id from it. The
// service does not parse the key; it stays a handle.
func ScheduledKey(archiveKey, schedule string) string {
	if schedule == "" {
		return archiveKey
	}
	return fmt.Sprintf("%s.s%x", archiveKey, sha256.Sum256([]byte(schedule)))[:len(archiveKey)+2+16]
}
