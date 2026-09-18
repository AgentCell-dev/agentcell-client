# Driving AgentCell from a coding harness

One static binary is both the CLI and the MCP server. A harness (Claude Code,
Codex, or anything speaking MCP stdio) holds a token and points at the control
plane; the 11 tools it sees are generated from `contract/registry.go`'s single
`Definitions` list, so CLI and MCP can never offer different verbs.

## Configuration

Claude Code (`claude mcp add-json`), or any client accepting an MCP stdio map:

```json
{
  "mcpServers": {
    "agentcell": {
      "command": "/path/to/agentcell",
      "args": ["mcp"],
      "env": {
        "AGENTCELL_API_URL": "<the control-plane URL you were given>",
        "AGENTCELL_TOKEN": "act_<prefix>_<secret>"
      }
    }
  }
}
```

Rules that matter:

- The token travels in `AGENTCELL_TOKEN` (or the platform token file — see
  `agentcell auth token`), never as an argument and never printed. The client
  redacts it from error text before rendering.
- `AGENTCELL_API_URL` is the control plane the token was issued against.
  Today that address is on the tailnet only — a harness outside the tailnet
  cannot reach it. Once the public route is live (`https://api.agentcell.cloud`),
  a harness anywhere will be able to reach it with the same token; until then,
  do not expect that address to answer.
- `tools/list` returns the 11 verbs (deploy, logs, rollback, env, secrets,
  domains, share, access, ps, spend, destroy) with schemas derived from the
  `cli:` struct tags. `deploy`, `logs` and `ps` are implemented; the other
  eight verbs answer a typed `not_found` naming what is missing — exit 12,
  branchable, never a stack trace.
- `deploy` takes `{"source": "<directory>", "cell": "<cell-id>"}`. The client
  archives the directory deterministically and derives
  `deploy-v1:<sha256>` as the idempotency key, so retrying a deploy from a new
  process is safe. Only `logs` streams.
- A token without the verb's scope is refused with `forbidden` (exit 11).
  Give the harness the narrowest scopes that cover its job — typically
  `deploy` + `read` — and keep `secrets`/`admin` tokens out of it
  (founding §6.1; see `docs/credentials.md`).

## The standing round-trip proof

`make mcp-harness` (infra repo) performs the whole trip against the live
service: `tools/list` showing the 11 verbs, a deploy of a small source
directory ending in status `deployed`, runtime logs streaming back through
`tools/call`, and the negative control — the same deploy with a `read`-only
token, refused as `forbidden`. Tokens are minted on a fixture org, revoked on
every exit path, and the harness cell is destroyed; the transcript is checked
for no credential appearing anywhere in it before the run passes — that check
is part of what "passing" means here, not a separate audit. See
`infra/scripts/mcp-harness-roundtrip.sh`.

The run has passed, live, twice in a row: `8 passed, 0 failed`, exit 0, each
run destroying its own cell and leaving no container on either worker. The
fixture org the harness deploys against, `cp-mcp-harness`, holds a Cloudflare
Access service token from the owner — without one the pipeline refuses to
publish the cell rather than leave an Access application with an empty
policy. Because the harness sends byte-identical source on every run, a
second run in a row would otherwise be answered from the first run's
idempotency record (`status=unchanged`, no build, no deploy); the harness
copies the fixture source and drops an inert `.harness-run` marker into the
copy so the archive hash, and therefore the idempotency key, differs between
runs and the second run performs a real deploy. See the STATUS.md entry
recording both runs, verbatim.

## Local surface check (no service needed)

`tools/list` is served from the compiled contract without contacting the
service, so the surface itself is checkable offline:

```sh
printf '%s\n' \
  '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}' \
  '{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}' \
  '{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}' \
  | AGENTCELL_TOKEN=... agentcell mcp
```

The response lists all 11 tools with their input schemas. (Any non-empty
`AGENTCELL_TOKEN` starts the server; `tools/list` exercises no credential.
Every other tool call authenticates against the control plane.)
