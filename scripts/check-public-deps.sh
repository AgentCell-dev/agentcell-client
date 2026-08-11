#!/bin/sh
set -eu

root=${1:-.}
cd "$root"

dependencies=$(go list -deps -f '{{if not .Standard}}{{.ImportPath}}{{end}}' ./...)
if printf '%s\n' "$dependencies" | grep -E '^github\.com/agentcell/agentcell(/|$)' >/dev/null; then
	echo "FAIL: public dependency graph imports private github.com/agentcell/agentcell"
	printf '%s\n' "$dependencies" | grep -E '^github\.com/agentcell/agentcell(/|$)'
	exit 1
fi
echo "PASS: public dependency graph contains no private AgentCell module"

