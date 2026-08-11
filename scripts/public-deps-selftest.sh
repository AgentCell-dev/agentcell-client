#!/bin/sh
set -eu

root=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT HUP INT TERM
mkdir -p "$tmp/public" "$tmp/private/secret" "$tmp/public/privateprobe"
cp -R "$root"/. "$tmp/public/"

printf '%s\n' 'module github.com/agentcell/agentcell' 'go 1.25' > "$tmp/private/go.mod"
printf '%s\n' 'package secret' 'const Topology = "private"' > "$tmp/private/secret/secret.go"
printf '%s\n' 'package privateprobe' 'import _ "github.com/agentcell/agentcell/secret"' > "$tmp/public/privateprobe/probe.go"
printf '\nrequire github.com/agentcell/agentcell v0.0.0\nreplace github.com/agentcell/agentcell => %s\n' "$tmp/private" >> "$tmp/public/go.mod"

if "$tmp/public/scripts/check-public-deps.sh" "$tmp/public" >/dev/null 2>&1; then
	echo "FAIL: dependency boundary accepted a private import"
	exit 1
fi
echo "PASS: dependency boundary rejected an introduced private import"

