#!/bin/sh
set -eu

base=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)
verifier=$(CDPATH= cd -- "$(dirname "$0")" && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT HUP INT TERM
export GOCACHE="$tmp/go-cache"

expect_failure() {
  label=$1
  pattern=$2
  shift 2
  set +e
  output=$("$@" 2>&1)
  status=$?
  set -e
  test "$status" -ne 0 || { echo "FAIL $label: mutant passed"; exit 1; }
  printf '%s\n' "$output" | grep "$pattern" >/dev/null || { printf '%s\n' "$output"; echo "FAIL $label: wrong failure reason"; exit 1; }
  printf '%s\n' "$output" | grep "$pattern" | head -1
  echo "PASS $label: verification went red for the intended reason"
}

echo "--- surface parity mutant: hard-code MCP to one tool ---"
cp -R "$base" "$tmp/parity-client"
perl -0pi -e 's/func Tools\(definitions \[\]contract\.Definition\) \[\]Tool \{/func Tools(definitions []contract.Definition) []Tool {\n\tdefinitions = contract.Definitions[:1] \/\/ MUTANT: hard-coded MCP surface/' "$tmp/parity-client/internal/mcp/server.go"
expect_failure parity 'MCP did not gain probe' sh -c "cd '$tmp/parity-client' && go test ./internal/mcp -run TestAddingOneDefinitionAddsCLIAndMCP -count=1"

echo "--- version-header mutant: remove header from every request ---"
cp -R "$base" "$tmp/header-client"
cp -R "$verifier" "$tmp/header-verifier"
perl -0pi -e 's/\n\thttpRequest\.Header\.Set\(contract\.APIVersionHeader, contract\.APIVersion\)//' "$tmp/header-client/operations/http.go"
(cd "$tmp/header-verifier" && go mod edit -replace "github.com/AgentCell-dev/agentcell-client=$tmp/header-client")
expect_failure header 'version header = ""' sh -c "cd '$tmp/header-verifier' && go test -run TestEveryNonStreamingRequestCarriesVersionAndTypedBody -count=1"

echo "--- streaming mutant: buffer the complete response before scanning ---"
cp -R "$base" "$tmp/stream-client"
cp -R "$verifier" "$tmp/stream-verifier"
perl -0pi -e 's/\n\tscanner := bufio\.NewScanner\(response\.Body\)/\n\tall, _ := io.ReadAll(response.Body)\n\tresponse.Body = io.NopCloser(bytes.NewReader(all))\n\tscanner := bufio.NewScanner(response.Body)/' "$tmp/stream-client/operations/http.go"
(cd "$tmp/stream-verifier" && go mod edit -replace "github.com/AgentCell-dev/agentcell-client=$tmp/stream-client")
expect_failure streaming 'first log was buffered' sh -c "cd '$tmp/stream-verifier' && go test -run TestLogsEmitsFirstRecordBeforeFinalRecordExists -count=1 -timeout=5s"

echo "--- archive mutant: preserve source mtimes ---"
cp -R "$base" "$tmp/archive-client"
cp "$verifier/fixtures/archive_external_test.go" "$tmp/archive-client/internal/archive/verification_external_test.go"
perl -0pi -e 's/\n\t\theader\.ModTime, header\.AccessTime, header\.ChangeTime = time\.Unix\(0, 0\), time\.Time\{\}, time\.Time\{\}//' "$tmp/archive-client/internal/archive/archive.go"
expect_failure archive 'archive bytes differ' sh -c "cd '$tmp/archive-client' && go test -tags archiveoverlay ./internal/archive -run TestArchiveIsIndependentOfParentCreationOrderAndMtime -count=1"

echo "--- output-mode mutant: invert automatic TTY selection ---"
cp -R "$base" "$tmp/output-client"
cp "$verifier/fixtures/cli_output_external_test.go" "$tmp/output-client/internal/cli/verification_output_external_test.go"
perl -0pi -e 's/if r\.StdoutTTY \{\n\t\t\tmode = "human"\n\t\t\} else \{\n\t\t\tmode = "json"\n\t\t\}/if r.StdoutTTY {\n\t\t\tmode = "json"\n\t\t} else {\n\t\t\tmode = "human"\n\t\t}/' "$tmp/output-client/internal/cli/run.go"
expect_failure output 'not JSON\|not human output' sh -c "cd '$tmp/output-client' && go test -tags clioutputoverlay ./internal/cli -run 'TestSuccessfulOutputModeSelectionAndOverrides|TestLogsMachineOutputIsOneJSONObjectPerLine' -count=1"
