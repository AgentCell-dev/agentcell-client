package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/AgentCell-dev/agentcell-client/contract"
)

type failingOperations struct{}

func (failingOperations) Execute(context.Context, contract.Request) (contract.Response, error) {
	return nil, &contract.APIError{Code: contract.CodeDeploy, Message: "build exited 1", Hint: "fix the Dockerfile RUN command and redeploy"}
}
func (failingOperations) Stream(context.Context, contract.Request, func(contract.Response) error) error {
	return nil
}

func TestFailingDeployIsAgentLegible(t *testing.T) {
	dir := t.TempDir()
	var stderr bytes.Buffer
	runner := Runner{Operations: failingOperations{}, Out: &bytes.Buffer{}, Err: &stderr, StdoutTTY: false}
	exit := runner.Run(context.Background(), []string{"deploy", dir})
	if exit == 0 {
		t.Fatal("failing deploy exited zero")
	}
	var api contract.APIError
	if err := json.Unmarshal([]byte(strings.TrimSpace(stderr.String())), &api); err != nil {
		t.Fatalf("not JSON: %v: %s", err, stderr.String())
	}
	if api.Code != contract.CodeDeploy || api.Hint == "" {
		t.Fatalf("error not agent-legible: %+v", api)
	}
	if exit != contract.ExitCode(contract.CodeDeploy) {
		t.Fatalf("exit=%d", exit)
	}
}
