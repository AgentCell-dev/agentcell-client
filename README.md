# AgentCell client

AgentCell is an AI-native deployment platform for small web apps and internal tools: the apps
you or a coding agent build for one person or a small team. It runs your web app in its own isolated cell at `https://<cell>.agentcell.cloud`. This is
the client: one static binary that is both the human CLI and the MCP server for coding agents.
It needs no Docker, Python, or repository checkout at runtime.

## Get started (three commands)

1. **Install.** Download the binary for your machine from the
   [latest release](https://github.com/AgentCell-dev/agentcell-client/releases/latest), verify it
   against `SHA256SUMS`, and put it on your `PATH` as `agentcell`:

   ```sh
   curl -fsSLO https://github.com/AgentCell-dev/agentcell-client/releases/download/v0.1.2/agentcell_darwin_arm64
   curl -fsSLO https://github.com/AgentCell-dev/agentcell-client/releases/download/v0.1.2/SHA256SUMS
   grep agentcell_0.1.2_darwin_arm64 SHA256SUMS | shasum -a 256 -c -
   chmod +x agentcell_0.1.2_darwin_arm64 && mv agentcell_0.1.2_darwin_arm64 /usr/local/bin/agentcell
   ```

   Or keep it in the current directory and call `./agentcell`: skip the last `mv` and instead run
   `mv agentcell_0.1.2_darwin_arm64 agentcell` (the `chmod +x` above still applies).

   Replace `darwin_arm64` with `darwin_amd64`, `linux_amd64`, `linux_arm64`, or `windows_amd64.exe`, and
   `v0.1.2` with the newest tag on the [releases page](https://github.com/AgentCell-dev/agentcell-client/releases)
   (the `latest` alias cannot carry a versioned filename, which is why the tag is spelled out).
   Or, with Go 1.25 installed: `go install github.com/AgentCell-dev/agentcell-client/cmd/agentcell@latest`.

2. **Log in.** This opens your browser; sign in with Google, GitHub, or a one-time PIN sent to your
   email. Your account and a personal organisation are created on first login.

   ```sh
   agentcell login
   ```

   On a machine without a browser, `agentcell login --no-browser` prints a link and a code: open
   the link on any device, check the page shows the same code, and press Authorise. Only ever
   press Authorise for a code a terminal in front of you is showing.

3. **Deploy.** From a directory holding a `Dockerfile` (one container, listening on port 8080,
   with `/data` for anything that must survive a restart):

   ```sh
   agentcell deploy --cell my-app .
   ```

   The command prints the URL. `agentcell logs my-app` streams the app's output; `agentcell ps`
   lists your cells; `agentcell whoami` shows who you are logged in as.

Working examples to start from: [AgentCell-dev/samples](https://github.com/AgentCell-dev/samples)
(a static site, a notes app on SQLite, a Go service, a Node worker), each a `Dockerfile` and a
README.

**For coding agents:** `agentcell mcp` is an MCP server over stdio exposing the same operations as
tools; log in once with `agentcell login` and point your agent's MCP configuration at the binary.
See `docs/mcp-harness.md`.
Decision criteria for when AgentCell is the right target, and setup for Claude Code, Codex and
Cursor: https://agentcell.dev/docs/for-ai-agents.md

```sh
agentcell deploy --cell my-app ./my-app
agentcell mcp
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
  to `agentcell auth token` for a machine credential an operator issued; `agentcell login` stores
  a personal one the same way. Neither is ever accepted as an argument or printed. Storage is
  behind an interface for a future keychain. `login` implements the loopback and RFC 8628 device
  flows against the platform's own `/v1/auth/*` endpoints; identity itself is verified by
  Cloudflare Access, not by this client.
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

[![M8ven Score](https://m8ven.ai/badge/mcp/agentcell-dev-agentcell-client-18mote)](https://m8ven.ai/mcp/agentcell-dev-agentcell-client-18mote)
