#!/bin/sh
set -eu

client=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT HUP INT TERM
mkdir -p "$tmp/public" "$tmp/private/secret" "$tmp/public/privateprobe"
cp -R "$client"/. "$tmp/public/"
printf '%s\n' 'module github.com/agentcell/agentcell' 'go 1.25' > "$tmp/private/go.mod"
printf '%s\n' 'package secret' 'const Topology = "private"' > "$tmp/private/secret/secret.go"
printf '%s\n' 'package privateprobe' 'import _ "github.com/agentcell/agentcell/secret"' > "$tmp/public/privateprobe/probe.go"
printf '\nrequire github.com/agentcell/agentcell v0.0.0\nreplace github.com/agentcell/agentcell => %s\n' "$tmp/private" >> "$tmp/public/go.mod"

listed=$(cd "$tmp/public" && go list -deps -f '{{if not .Standard}}{{.ImportPath}}{{end}}' ./...)
printf '%s\n' "$listed" | grep -x 'github.com/agentcell/agentcell/secret' >/dev/null
echo "PASS negative fixture resolves: go list includes github.com/agentcell/agentcell/secret"
set +e
output=$("$tmp/public/scripts/check-public-deps.sh" "$tmp/public" 2>&1)
status=$?
set -e
test "$status" -eq 1
printf '%s\n' "$output" | grep 'public dependency graph imports private' >/dev/null
printf '%s\n' "$output"
echo "PASS gate failed for the intended private-graph reason, not module resolution"

