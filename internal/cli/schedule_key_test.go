package cli

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/AgentCell-dev/agentcell-client/contract"
)

// The service's own format check on the Idempotency-Key header (control-plane.py IDEMPOTENCY_KEY).
// It does not parse the key -- it stays a handle -- so this is the only property it demands.
var serviceKeyFormat = regexp.MustCompile(`^[A-Za-z0-9:._-]{1,200}$`)

func bindDeploy(t *testing.T, args ...string) *contract.DeployRequest {
	t.Helper()
	definition, ok := contract.Lookup("deploy")
	if !ok {
		t.Fatal("no deploy definition")
	}
	request, apiErr := Parse(definition, args)
	if apiErr != nil {
		t.Fatalf("Parse(%q): %+v", args, apiErr)
	}
	return request.(*contract.DeployRequest)
}

func sourceDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestDeployKeyWithoutScheduleIsUnchanged: SCHEDULED-CELLS.md §2.2. A deploy with no --schedule
// must carry EXACTLY the key 0.1.2 derived -- `deploy-v1:` and the hex sha256 of the archive bytes,
// restated here from the formula rather than from the code under test -- or every web deploy a
// partner retries across the upgrade stops replaying and builds again.
func TestDeployKeyWithoutScheduleIsUnchanged(t *testing.T) {
	request := bindDeploy(t, sourceDir(t))
	want := fmt.Sprintf("deploy-v1:%x", sha256.Sum256(request.Source))
	if request.IdempotencyKey != want {
		t.Fatalf("key without a schedule moved: got %q, want the 0.1.2 key %q", request.IdempotencyKey, want)
	}
	if request.Schedule != "" {
		t.Fatalf("schedule = %q with no --schedule", request.Schedule)
	}
}

// TestDeployKeyCoversSchedule: SCHEDULED-CELLS.md §2.2, and the S-1 negative control. With the key
// a digest of the archive alone, `deploy --schedule "0 2 * * *"` then `deploy --schedule "0 3 * * *"`
// on the same directory send the SAME key, the service replays the first as `unchanged`, and the
// schedule never changes. Different schedules must give different keys; the same schedule the same
// key; and every key must still pass the service's format check.
func TestDeployKeyCoversSchedule(t *testing.T) {
	dir := sourceDir(t)
	two := bindDeploy(t, "--schedule", "0 2 * * *", dir)
	three := bindDeploy(t, dir, "--schedule", "0 3 * * *")
	twoAgain := bindDeploy(t, "--schedule=0 2 * * *", dir)
	if two.Schedule != "0 2 * * *" || three.Schedule != "0 3 * * *" {
		t.Fatalf("schedule not bound: %q, %q", two.Schedule, three.Schedule)
	}
	if two.IdempotencyKey == three.IdempotencyKey {
		t.Fatalf("two schedules on one archive share the key %q: the second deploy would replay as unchanged", two.IdempotencyKey)
	}
	if two.IdempotencyKey != twoAgain.IdempotencyKey {
		t.Fatalf("the same schedule gave two keys: %q, %q", two.IdempotencyKey, twoAgain.IdempotencyKey)
	}
	base := fmt.Sprintf("deploy-v1:%x", sha256.Sum256(two.Source))
	want := fmt.Sprintf("%s.s%x", base, sha256.Sum256([]byte("0 2 * * *")))[:len(base)+2+16]
	if two.IdempotencyKey != want {
		t.Fatalf("scheduled key = %q, want %q", two.IdempotencyKey, want)
	}
	for _, key := range []string{two.IdempotencyKey, three.IdempotencyKey} {
		if !serviceKeyFormat.MatchString(key) {
			t.Fatalf("key %q fails the service's format check", key)
		}
	}
}
