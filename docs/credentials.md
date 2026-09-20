# The two credentials

AgentCell has two machine credentials, for two different doors. They look
alike on purpose (both are `act_<prefix>_<secret>`, both shown exactly once at
creation) and they are not interchangeable. Presenting one at the other's door
fails closed: the control plane answers `unauthenticated`, the Worker refuses.

## Log in

**If you are a person, this is how you get an API token — not the owner
minting one for you.** Run:

```sh
agentcell login
```

This opens your browser to `login.agentcell.cloud`, where Cloudflare Access
verifies you (Google, GitHub, or a one-time PIN), and the CLI collects and
stores an API token for you automatically — the token itself never appears
in the browser, a URL, or your terminal's scrollback. First login creates a
personal organisation; every login prints `logged in as <email>, org
<org_id>, plan <plan>`. `agentcell whoami` shows who you are signed in as,
and `agentcell logout` revokes the credential and removes it locally.

**If your terminal has no browser** — an SSH session, a container, a CI
runner used interactively — run `agentcell login --no-browser`. The CLI
prints a short URL and an eight-character code; open that URL on any device
that does have a browser, sign in there, and type the code. Only typing the
code binds that sign-in to the machine that asked for it.

**The one thing to know before you type a code on that page**: type a code
only if it came from a terminal you are looking at yourself. A code somebody
else sends you and asks you to enter signs THEM in as you, the same way it
would for `gcloud auth login` or `gh auth login --web` — no link the CLI
prints can do this on its own, only a person's own typing past the page's
warning can (SIGNUP.md §8 item 9 records this as a known, unavoidable
residual of the device-authorization shape, not a defect of this
implementation).

### Open your app

On first login your email is granted to your organisation's cell gate, so
opening the printed URL for a cell you deployed asks you to sign in the same
way you just did for `agentcell login`, then shows the app — there is
nothing separate to set up. That is a person reaching their own cell in a
browser; it is not the credential a program needs to reach one
programmatically. **Machines reaching a cell need the gate's service token**
(`agentcell share`, owner-scoped) — see §2 below for how that token differs
from the API token above.

A plan of `waitlist` means the org is not yet approved to deploy — `login`,
`whoami`, `ps` and `logs` all work, and `deploy` answers a typed
`plan_required` refusal until the owner approves the org.

**Machines still use a minted token** (CI runners, agents with no human at
the keyboard): see §1 below. `agentcell login` is for a person sitting at a
terminal; nothing mints a self-service token for a process that isn't one.

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

How to get one, as a person: run `agentcell login` (see "Log in" above).

How to get one, as a machine (CI, an agent with no human present): there is
no self-serve issuance for these — the owner mints it and gives you the
value once:

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
