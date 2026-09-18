# AgentCell client

The public, topology-blind AgentCell client. One static Go binary provides the human CLI and the
agent-facing MCP server. It needs no Docker, Python, or repository checkout at runtime.

```sh
AGENTCELL_TOKEN=... agentcell deploy ./my-app
AGENTCELL_TOKEN=... agentcell mcp
```

When stdout is a terminal output is human-readable; otherwise it is JSON. `--output=human|json`
overrides detection. `logs` is the one streaming operation and emits one JSON object per line in
machine mode.

## Credentials and the harness

Two credentials open two different doors — an API token (`deploy`/`read`/
`secrets`/`admin`) goes to the control plane, a `cells`-scoped token goes to
a cell hostname and is verified by the auth Worker. They are not
interchangeable. See `docs/credentials.md` for which is which and how to get
each, and `docs/mcp-harness.md` for configuring a real coding harness against
`agentcell mcp` (config snippet, the 11-tool surface, and the standing
deploy-and-logs round-trip proof).

## Contract and implementation boundary

`contract.Definitions` is the only verb declaration. Each entry has its name, summary, concrete Go
request and response types, streaming bit, and destructive bit. CLI help/commands, argument binding,
MCP tools/descriptions/schemas, and HTTP routes all derive from it. `contract.Operations` has two
methods because streaming changes lifetime: `Execute` for request/response operations and `Stream`
for logs. The public `operations.HTTPClient` implements it; the private service implements the same
interface directly for in-process tests. No framework is needed for this boundary.

Destructive operations require an explicit request confirmation and, at the service boundary, a
separately scoped capability token. Today only `destroy` is irreversible and therefore marked
destructive; `confirm` must exactly equal `cell_id`. Mutating access, secret, and domain operations
still require their own service-side capabilities. A deploy-only token must never authorize those
verbs. The MCP server does not weaken this rule despite holding a standing token.

## Deliberate protocol decisions

- API version: every request sends `AgentCell-Version: 2026-08-01`. A service supports a dated
  version for its compatibility window or returns `unsupported_version` with both versions. A
  breaking wire change gets a new date; adding optional fields or verbs is compatible.
- Idempotency: deterministic tar+gzip bytes are hashed as `deploy-v1:<sha256>` and sent both in the
  typed request and `Idempotency-Key`. The service must enforce uniqueness per organization and
  return the recorded deployment for a duplicate key. This survives an agent starting a new process
  to retry; a random per-process key would not.
- Streaming: only `logs` is declared streaming. It uses newline-delimited JSON over HTTP and the
  separate `Operations.Stream` method.
- Tokens: `AGENTCELL_TOKEN` wins. Otherwise the file is under `$XDG_CONFIG_HOME/agentcell`,
  `~/.config/agentcell` on Unix, or the platform config directory on Windows. Pipe an interim token
  to `agentcell auth token`; it is never accepted as an argument or printed. Storage is behind an
  interface for a future keychain. OAuth device flow is intentionally not hand-rolled; when issued
  tokens replace interim SOPS tokens it must use a maintained library.
- Dependencies: the current client is standard-library-only, so there is no `go.sum` yet. Once a
  module is added, `go.sum` is the required version-and-hash pin; no second hash mechanism is needed.

## Gates and release

```sh
make lint test lint-selftest
make release VERSION=0.1.0
```

The dependency gate inspects the complete Go dependency graph and rejects the private
`github.com/agentcell/agentcell` module. Its self-test introduces a resolvable private module and
proves rejection. Surface parity is tested by appending one definition and requiring both CLI and
MCP to gain it. Releases require exactly Go 1.25.5, cross-compile static binaries twice, compare the
bytes, and publish `SHA256SUMS` beside them.
