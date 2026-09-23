# Credentials

AgentCell has two kinds of machine credential, for two different doors: an
API token, presented to the control plane, and a `cells`-scoped token,
presented to a cell's own hostname. They look alike (both are
`act_<prefix>_<secret>`, both shown exactly once) and they are not
interchangeable — presenting one at the other's door fails closed.

## Log in

```sh
agentcell login
```

This opens your browser to `login.agentcell.cloud`, where Cloudflare Access
verifies you (Google, GitHub, or a one-time PIN), and the CLI collects and
stores an API token for you — the token itself never appears in the browser,
a URL, or your terminal's scrollback. First login creates a personal
organisation and mints a token scoped `deploy`, `read` and `admin` for it;
every login prints `logged in as <email>, org <org_id>, plan <plan>`.
`agentcell whoami` shows who you are signed in as. `agentcell logout` revokes
the stored token server-side and removes it locally.

On first login your email is also admitted to your organisation's cell
gate, which is what lets a browser open a cell you deployed without a
separate setup step (see "Open your app" below).

If your terminal has no browser — an SSH session, a container, a CI runner
used interactively — run `agentcell login --no-browser`. The CLI prints a
link, then an eight-character code, then a plain URL. Open the link on any
device that does have a browser and sign in there; the page shows the code
it is about to authorise. Compare the code with the one in your terminal and,
if they match, press Authorise — the terminal finishes logging in by itself.
If you cannot follow the link (a screenshot, a hardware terminal), open the
plain URL instead and type the code.

**The one thing to know before you press Authorise on that page:** press
Authorise only if a terminal in front of you is showing that code. A link or
code somebody else sends you and asks you to approve signs THEM in as you —
the same risk `gcloud auth login` and `gh auth login --web` carry with their
own device codes. Opening the link does nothing on its own; only a person
pressing Authorise past the page's warning does. If you did not start the
login, press Cancel on that page: the code stops working for everyone.

## Open your app

Every cell sits behind a sign-in gate at its own hostname
(`https://<cell>.agentcell.cloud`). A browser reaching that gate signs in the
same way `agentcell login` does — there is nothing separate to configure for
a person opening a cell they can already see with `agentcell ps`.

A machine reaching a cell — a `curl`, an agent, a webhook — cannot go
through that browser sign-in, and needs the organisation's service token
instead. `agentcell share` is meant to be the self-service path (it grants a
subject a role on a cell: `agentcell share --cell <id> --subject <user-or-
group> --role <role>`), but on the currently deployed control plane this
verb is not live yet — it answers `not_found`, because the access-grant
projection it depends on is not pushed automatically. Until it lands, ask
the operator to issue your organisation's service token; the contact is the
address on agentcell.dev.

## Plans

A new organisation lands on `design-partner` when the platform's self-signup
is set to approve automatically, which is the case today, and such an
organisation can deploy immediately. When self-signup approval is off
instead, a new organisation starts on `waitlist`: `login`, `whoami`, `ps`
and `logs` all work, but `deploy` answers `forbidden` with the reason
`plan_required` until the operator approves the organisation. Either way,
ask by writing to the address on agentcell.dev.

## Machine credentials

An API token is presented to the control plane
(`Authorization: Bearer <token>`) and carries one or more scopes:

- `deploy` — deploy, rollback, apply non-secret environment changes.
- `read` — logs, list your cells, see spend.
- `admin` — the four organisation-level operations (domains, share, access,
  destroy) on the organisation the token's own row names. It is scoped to
  your organisation, never to the platform — an `admin` token cannot reach
  another organisation's cells, and it is a different thing from the
  platform's own administrative access, which no token grants.
- `secrets` — reading or writing stored secrets. It is its own scope, kept
  separate on purpose so that a `deploy` credential can never also read
  secrets, and it is not among the scopes `agentcell login` mints for you:
  a personal login token carries `deploy`, `read` and `admin` only.

A token is presented as `AGENTCELL_TOKEN` in the environment, or read from
the file the CLI stores under your platform's config directory (`$XDG_CONFIG_
HOME/agentcell`, `~/.config/agentcell` on Unix, or the platform equivalent on
Windows) — `agentcell login` writes that file for you, and `agentcell auth
token` writes it for a machine credential someone else minted and piped to
you. A token is shown exactly once, at creation; the CLI never prints a
stored or already-issued token back to you.

To revoke your own token, run `agentcell logout`. There is not yet a CLI verb
for revoking another token issued to your organisation (a CI runner's, say);
ask the operator to revoke it until one exists.

## The cell gate credential

A cell's own hostname is a second, separate door from the control plane. The
credential that door checks is a `cells`-scoped token, verified at the cell
hostname by an auth Worker against a per-organisation projection — not by
the control plane, and not implied by an `admin` token, because a credential
presented to a tenant-facing hostname has to be asked for exactly, never
received as a side effect of a broader grant.

This is not yet the credential actually enforced in production: today, cell
access is still gated by Cloudflare Access rather than by the Worker, and
the working machine credential is the per-organisation Access service token
described under "Open your app" above. The `cells`-scoped token exists in
the platform's token model already and becomes the credential a request is
actually checked against once the Worker becomes the routed gate; there is
an open date recorded internally for when the current interim credential
stops being issued, but no public commitment yet on when the Worker cutover
itself happens — ask the operator if your integration depends on the
timing.
