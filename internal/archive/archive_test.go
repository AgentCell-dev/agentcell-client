package archive

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
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
