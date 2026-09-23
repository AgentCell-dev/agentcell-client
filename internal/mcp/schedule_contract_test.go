package mcp

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/AgentCell-dev/agentcell-client/contract"
	"github.com/AgentCell-dev/agentcell-client/internal/cli"
)

// TestEveryRequestFieldIsOnBothSurfaces is FIELD parity, beside the VERB parity the tests in
// surface_test.go prove. Both surfaces derive from the request struct's `cli:` tags (Tools() here,
// OperationHelp() in internal/cli), so a field on one and not the other means one of them stopped
// reading the tag. SCHEDULED-CELLS.md §2.1 names `schedule` on `deploy` in particular: it is checked
// by name as well, so the field cannot leave both surfaces together unnoticed either.
func TestEveryRequestFieldIsOnBothSurfaces(t *testing.T) {
	tools := Tools(contract.Definitions)
	for i, definition := range contract.Definitions {
		help := cli.OperationHelp(definition)
		for name := range tools[i].InputSchema.Properties {
			if !strings.Contains(help, "--"+name+" ") && !strings.Contains(help, "<"+name+">") {
				t.Errorf("%s: MCP tool has %q, `agentcell help %s` does not:\n%s", definition.Name, name, definition.Name, help)
			}
		}
		for _, line := range strings.Split(help, "\n") {
			if !strings.HasPrefix(line, "  --") {
				continue
			}
			name := strings.Fields(strings.TrimPrefix(line, "  --"))[0]
			if _, ok := tools[i].InputSchema.Properties[name]; !ok {
				t.Errorf("%s: `agentcell help %s` has --%s, the MCP tool does not", definition.Name, definition.Name, name)
			}
		}
	}
	deploy, ok := contract.Lookup("deploy")
	if !ok {
		t.Fatal("no deploy definition")
	}
	var tool *Tool
	for i := range tools {
		if tools[i].Name == "deploy" {
			tool = &tools[i]
		}
	}
	if tool == nil {
		t.Fatal("no deploy tool")
	}
	property, ok := tool.InputSchema.Properties["schedule"]
	if !ok || property.Type != "string" {
		t.Fatalf("deploy tool schema lacks a string `schedule` property: %+v", tool.InputSchema.Properties)
	}
	for _, required := range tool.InputSchema.Required {
		if required == "schedule" {
			t.Fatal("`schedule` is required on the deploy tool; a web deploy sends none")
		}
	}
	help := cli.OperationHelp(deploy)
	if !strings.Contains(help, "[--schedule]") || !strings.Contains(help, "  --schedule ") {
		t.Fatalf("`agentcell help deploy` does not carry --schedule:\n%s", help)
	}
	if !strings.Contains(help, "UTC") || !strings.Contains(help, "5 minutes") {
		t.Fatalf("`agentcell help deploy` does not state the schedule's zone and floor:\n%s", help)
	}
}

// TestLastExitCodeZeroIsReportedAndAbsentIsAbsent: SCHEDULED-CELLS.md §2.1. A run that exited 0 and
// a run with no exit code (running, unplaced, unknown) are different facts, and an `int` with
// omitempty would erase the first into the second. The MCP tool result is json.Marshal of the
// response, so this is what an agent reads.
func TestLastExitCodeZeroIsReportedAndAbsentIsAbsent(t *testing.T) {
	zero := `{"cells":[{"cell_id":"c","status":"active","url":"","kind":"scheduled","last_run_status":"succeeded","last_exit_code":0}]}`
	var decoded contract.PSResponse
	if err := json.Unmarshal([]byte(zero), &decoded); err != nil {
		t.Fatal(err)
	}
	if code := decoded.Cells[0].LastExitCode; code == nil || *code != 0 {
		t.Fatalf("last_exit_code 0 decoded as %v, want a pointer to 0", code)
	}
	again, _ := json.Marshal(decoded)
	if !strings.Contains(string(again), `"last_exit_code":0`) {
		t.Fatalf("exit 0 did not survive the round trip: %s", again)
	}

	absent := `{"cells":[{"cell_id":"c","status":"active","url":"","kind":"scheduled","last_run_status":"running"}]}`
	decoded = contract.PSResponse{}
	if err := json.Unmarshal([]byte(absent), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Cells[0].LastExitCode != nil {
		t.Fatalf("absent last_exit_code decoded as %d, want nil", *decoded.Cells[0].LastExitCode)
	}
	again, _ = json.Marshal(decoded)
	if strings.Contains(string(again), "last_exit_code") {
		t.Fatalf("absent exit code was invented on the way out: %s", again)
	}

	// A web cell's row is exactly the three fields it always had: nothing scheduled leaks in.
	web, _ := json.Marshal(contract.CellStatus{CellID: "c", Status: "active", URL: "https://c.invalid"})
	if string(web) != `{"cell_id":"c","status":"active","url":"https://c.invalid"}` {
		t.Fatalf("a web cell's status grew fields: %s", web)
	}
}
