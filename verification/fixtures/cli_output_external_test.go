//go:build clioutputoverlay

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/AgentCell-dev/agentcell-client/contract"
)

type outputOperations struct{}

func (outputOperations) Execute(context.Context, contract.Request) (contract.Response, error) {
	return &contract.PSResponse{Cells: []contract.CellStatus{{CellID: "cell-a", Status: "awake", URL: "https://cell.invalid"}}}, nil
}
func (outputOperations) Stream(context.Context, contract.Request, func(contract.Response) error) error {
	return nil
}

type logOperations struct{}

func (logOperations) Execute(context.Context, contract.Request) (contract.Response, error) {
	return nil, nil
}
func (logOperations) Stream(_ context.Context, _ contract.Request, emit func(contract.Response) error) error {
	for _, entry := range []contract.LogEntry{{Time: "t1", Stream: "stdout", Message: "first"}, {Time: "t2", Stream: "stderr", Message: "last"}} {
		if err := emit(&entry); err != nil {
			return err
		}
	}
	return nil
}

func TestSuccessfulOutputModeSelectionAndOverrides(t *testing.T) {
	cases := []struct {
		name     string
		tty      bool
		args     []string
		jsonMode bool
	}{
		{name: "pipe defaults JSON", args: []string{"ps"}, jsonMode: true},
		{name: "TTY defaults human", tty: true, args: []string{"ps"}},
		{name: "TTY JSON override", tty: true, args: []string{"--output=json", "ps"}, jsonMode: true},
		{name: "pipe human override", args: []string{"--output=human", "ps"}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			exit := (Runner{Operations: outputOperations{}, Out: &stdout, Err: &stderr, StdoutTTY: test.tty}).Run(context.Background(), test.args)
			if exit != 0 || stderr.Len() != 0 {
				t.Fatalf("exit=%d stderr=%q", exit, stderr.String())
			}
			if test.jsonMode {
				var decoded contract.PSResponse
				if err := json.Unmarshal(stdout.Bytes(), &decoded); err != nil {
					t.Fatalf("not JSON: %q: %v", stdout.String(), err)
				}
			} else if got := stdout.String(); got[:6] != "cells:" {
				t.Fatalf("not human output: %q", got)
			}
		})
	}
}

func TestLogsMachineOutputIsOneJSONObjectPerLine(t *testing.T) {
	var stdout, stderr bytes.Buffer
	exit := (Runner{Operations: logOperations{}, Out: &stdout, Err: &stderr, StdoutTTY: false}).Run(context.Background(), []string{"logs", "--cell", "cell-a"})
	if exit != 0 || stderr.Len() != 0 {
		t.Fatalf("exit=%d stderr=%q", exit, stderr.String())
	}
	lines := bytes.Split(bytes.TrimSpace(stdout.Bytes()), []byte("\n"))
	if len(lines) != 2 {
		t.Fatalf("got %d lines: %q", len(lines), stdout.String())
	}
	for index, line := range lines {
		var entry contract.LogEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			t.Fatalf("line %d is not JSON: %q", index, line)
		}
	}
}
