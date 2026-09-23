package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/AgentCell-dev/agentcell-client/contract"
)

// deployedOperations answers deploy with what the service sends since WP-60 (infra 84511ab): the
// container's port and, because the Dockerfile stated no EXPOSE, the hint saying 8080 was used.
type deployedOperations struct{ response contract.DeployResponse }

func (d deployedOperations) Execute(context.Context, contract.Request) (contract.Response, error) {
	response := d.response
	return &response, nil
}
func (deployedOperations) Stream(context.Context, contract.Request, func(contract.Response) error) error {
	return nil
}

const defaultPortHint = "The Dockerfile's final stage states no TCP port with EXPOSE, so the cell was deployed on the default, 8080."

// TestDeployOutputCarriesPortAndHint: the hint is on its own line in human output, and the JSON
// output carries port and hint as the service sent them. Before DeployResponse had the fields the
// client decoded the response into the old struct and dropped both, silently.
func TestDeployOutputCarriesPortAndHint(t *testing.T) {
	operations := deployedOperations{contract.DeployResponse{CellID: "cell-a", DeploymentID: "dep-1", URL: "https://cell-a.invalid", Status: "deployed", Port: 8080, Hint: defaultPortHint}}
	dir := t.TempDir()

	var human, stderr bytes.Buffer
	if exit := (Runner{Operations: operations, Out: &human, Err: &stderr, StdoutTTY: true}).Run(context.Background(), []string{"deploy", dir}); exit != 0 {
		t.Fatalf("exit=%d stderr=%q", exit, stderr.String())
	}
	lines := strings.Split(strings.TrimSpace(human.String()), "\n")
	hintLines := 0
	for _, line := range lines {
		if line == "hint: "+defaultPortHint {
			hintLines++
		}
	}
	if hintLines != 1 {
		t.Fatalf("human output does not carry the hint on its own line:\n%s", human.String())
	}
	if !strings.Contains(human.String(), "port: 8080\n") {
		t.Fatalf("human output does not carry the port:\n%s", human.String())
	}

	var machine bytes.Buffer
	if exit := (Runner{Operations: operations, Out: &machine, Err: &stderr, StdoutTTY: false}).Run(context.Background(), []string{"deploy", dir}); exit != 0 {
		t.Fatalf("exit=%d stderr=%q", exit, stderr.String())
	}
	var decoded map[string]any
	if err := json.Unmarshal(machine.Bytes(), &decoded); err != nil {
		t.Fatalf("not JSON: %q: %v", machine.String(), err)
	}
	if decoded["port"] != float64(8080) || decoded["hint"] != defaultPortHint {
		t.Fatalf("JSON output lost port or hint: %s", machine.String())
	}

	// Positive control beside it: an explicit EXPOSE sends no hint, and none is printed.
	operations.response.Hint, operations.response.Port = "", 3000
	human.Reset()
	if exit := (Runner{Operations: operations, Out: &human, Err: &stderr, StdoutTTY: true}).Run(context.Background(), []string{"deploy", dir}); exit != 0 {
		t.Fatalf("exit=%d stderr=%q", exit, stderr.String())
	}
	if strings.Contains(human.String(), "hint:") || !strings.Contains(human.String(), "port: 3000\n") {
		t.Fatalf("output without a hint:\n%s", human.String())
	}
}

// TestDeployOutputOmitsAnAbsentPort: a replay (`unchanged`) and a scheduled cell carry no port;
// the human output must not print "port: 0". Positive control beside it: a port that was sent is.
func TestDeployOutputOmitsAnAbsentPort(t *testing.T) {
	var human bytes.Buffer
	if err := render(&human, "human", &contract.DeployResponse{CellID: "c", DeploymentID: "d", URL: "https://c.invalid", Status: "unchanged"}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(human.String(), "port:") {
		t.Fatalf("an absent port was printed:\n%s", human.String())
	}
	human.Reset()
	if err := render(&human, "human", &contract.DeployResponse{CellID: "c", Status: "deployed", Port: 8080}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(human.String(), "port: 8080\n") {
		t.Fatalf("a sent port was not printed:\n%s", human.String())
	}
}
