# The two credentials

AgentCell has two machine credentials, for two different doors. They look
alike on purpose (both are `act_<prefix>_<secret>`, both shown exactly once at
creation) and they are not interchangeable. Presenting one at the other's door
fails closed: the control plane answers `unauthenticated`, the Worker refuses.

## 1. API token — for the control plane

Presented to the **control-plane API** (`AGENTCELL_API_URL`,
`POST /v1/operations/<verb>`, `Authorization: Bearer <token>`), by `agentcell`
CLI and `agentcell mcp` alike.

Scopes: `deploy`, `read`, `secrets`, `admin` (default `deploy` + `read`).
Each verb requires its scope — see `contract/registry.go` via
`tokens.SCOPES` on the service side — and `admin` implies the rest at the API.
Founding §6.1 is the rule: a deploy token must not read secrets or change
access lists, so a CI credential stays `deploy` (+ `read` for logs) and
`secrets`/`admin` tokens live with a human, not in a runner.

How to get one: there is no self-serve issuance. The owner mints it and gives
you the value once:

```sh
make cp-token-issue ORG=<org> SCOPE=deploy   # repeat --scope for more
make cp-token-ls ORG=<org>                   # prefixes, scopes, live/revoked
make cp-token-revoke PREFIX=<prefix>         # soft delete; never reinstated
```

The database holds a scrypt hash and a lookup prefix, never the secret —
`make cp-token-show-row ORG=<org>` prints the stored row to prove it.

## 2. `cells`-scoped token — for reaching cells

Presented to a **cell hostname** (`https://<cell>.agentcell.cloud`,
`Authorization: Bearer <token>`), and verified there by the auth Worker
(infra NEXT.md item 5 stage 2) against a Workers KV projection — not by the
control plane. It grants nothing at the API: `admin` does not imply it and it
is not in the API scope map, because a credential presented to tenant-facing
hostnames must be asked for exactly, never received as a side effect of a
broader grant.

How to get one: same rule as the API token, no self-serve — the owner mints
it:

```sh
make cp-token-issue ORG=<org> SCOPE=cells
```

Minting a `cells` token needs `CELL_TOKEN_KEY` (the HMAC key the Worker
verifies with, held in SOPS and as a Worker secret, never in Neon or KV), so
it runs through the same SOPS-wrapped path. The Worker admits the token to
that org's cells and no others; revocation converges through the KV
reprojection (`make access-reproject ORG=<org>`).

**This token is not yet the machine credential a cell actually checks.**
Until NEXT.md item 5's stages 4–5 land, cells are gated by Cloudflare Access,
not the Worker, and the interim machine credential is a per-org Access
service token the owner issues per org — the same mechanism `acme-design`,
`globex-labs` and the `cp-mcp-harness` fixture org hold today. The `cells`-
scoped token above is real and mintable now, but it becomes the credential a
request is actually checked against only at the stage-5 cutover, when the
Worker becomes the routed gate. NEXT.md records the interim's recorded
deadline as 31 October 2026; that date is when the interim expires, not when
the cutover is scheduled.

## Which is which, at a glance

| | API token | `cells` token |
|---|---|---|
| Presented to | control-plane API | cell hostname |
| Carried as | `Authorization: Bearer` | `Authorization: Bearer` |
| Verified by | control-plane service (scrypt row) | auth Worker (KV verifier) |
| Scopes | deploy / read / secrets / admin | cells (only) |
| Issued via | `make cp-token-issue ORG= SCOPE=<scope>` | `make cp-token-issue ORG= SCOPE=cells` |
| Grants at the other door | nothing | nothing |
