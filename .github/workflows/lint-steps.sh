#!/bin/sh
# Run by .github/workflows/lint.yml IN THIS REPOSITORY (AgentCell-dev/agentcell-client), and by
# hand for local reproduction — kept beside the workflow (WP-07, PRODUCTION.md "CI, lint tier")
# so the hosted runner and a local rerun execute the exact same bytes.
#
# This job is standalone on purpose: unlike infra's lint job, it never reaches for the private
# repository. Nothing this repository's `make lint` or `make lint-selftest` does depends on
# infra/ being present — see this repo's Makefile — so there is nothing here for a sibling clone
# to provide, and adding one would only be a second thing that can go stale.
#
# `make lint-selftest` needs exactly Go 1.25.5 (README.md's release note), which the workflow
# pins via actions/setup-go before calling this script.

set -eu

REPO_ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
cd "$REPO_ROOT"

echo "=== lint-steps.sh (agentcell-client) starting in $REPO_ROOT ==="
echo "=== go version: $(go version) ==="

echo "=== make lint ==="
make lint

echo "=== make lint-selftest ==="
make lint-selftest

echo "=== lint-steps.sh (agentcell-client): PASS ==="
