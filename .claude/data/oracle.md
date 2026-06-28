# Oracle — kilroy

Last updated: 2026-06-25T20:16Z

## Index
- kilroyHelp

---

## kilroyHelp

**Who:** Travis owns; the operator front-end for running kilroy on darkfactory.

**What:** An UNVERSIONED busybox-style multicall bash tool (dispatches on `$0`
basename) that wraps the kilroy run lifecycle, the cliproxyapi gateway, and the
usage-gate. Lives in the creds dir, deployed by scp to the host — NEVER committed
to the kilroy repo. The "provider" concept runs through several of its commands
because cliproxyapi now fronts BOTH Claude and OpenAI/Codex and the gate controls
both.

**When:** Use on darkfactory to log in upstreams, watch usage, run/park kilroy,
and inspect status. Don't edit it in the kilroy repo — it's not there; edit in
`creds/` and scp-deploy.

**Where:**
- Source (unversioned): `~/workspace/x85446/creds/kilroyHelp`
- Deployed: `/opt/darkfactory/bin/kilroyHelp` (PATH on darkfactory)
- Gate config: `/etc/kilroy-usage-gate.conf`
- Proxy config: `/etc/cliproxyapi.conf`; auths: `~/.cli-proxy-api/<type>-<email>.json`
- Architecture doc: `kilroy/docs/kilroy-cliproxy-architecture.md`

**Why:** cliproxyapi multiplexes Claude + Codex behind one endpoint with a
round-robin pool (alias `auto` → claude-opus-4-8 + gpt-5.5). kilroyHelp is how an
operator logs each provider in, sees each provider's budget, and lets the gate
fail over between them.

**How — the "provider" flag/arg appears in these commands (provider = `claude` | `codex`):**
- `kilroyHelp cliproxy login [claude|codex]` — re-auth one upstream's OAuth.
  Defaults to `claude`. `claude` = localhost-callback flow (stops the service to
  free the port); `codex` = device-code flow (`-codex-device-login`, polls, no
  port → service keeps serving). Verifies success by the `<type>-*.json` token
  mtime, not exit code.
- `kilroyHelp usage --provider claude|codex|both` — live 5h/7d utilization.
  claude ← Anthropic `/api/oauth/usage`; codex ← ChatGPT
  `backend-api/codex/usage` (primary_window=5h, secondary_window=7d). Default `both`.
- `kilroyHelp gate --check` — per-provider PARK/ALLOW verdict + RUN verdict.
- `kilroyHelp gate --tick` — ONE enforcement pass: apply pool failover (disable
  the gated provider's auth so the proxy drops it), no systemd actions. Safe to
  run anytime; honors injection `GATE_TEST_5H` (claude) / `GATE_TEST_CX_5H` (codex).
- `kilroyHelp status` — per-provider `auth <type>` line (ok / gated / refresh-due /
  disabled) + a `pool: serving via …` line.
- Config knob (not a flag): `PROVIDERS=claude codex` in
  `/etc/kilroy-usage-gate.conf` lists which providers the gate governs; one config,
  same burn-envelope thresholds for both. Run pauses only when ALL gated.
- Pool wiring (not a provider flag, but related): `KILROY_POOL_MODEL` (default
  `auto`) makes `kilroyHelp run` pass `--force-model anthropic=<alias>` so kilroy
  hits the pool. `resume` does NOT accept `--force-model` (keeps baked models).

**Gotchas:**
- The gated provider's auth `disabled` flag is the failover lever; cliproxyapi
  hot-reloads it (no restart). A `disabled` auth shows as `gated` in status when
  the gate is running, vs `disabled — re-login` when it isn't.
- Don't restart cliproxyapi to apply config — it has a file-watcher that
  hot-reloads `/etc/cliproxyapi.conf` and the auth dir.
- OAuth tokens must never be printed; the probes read them into locals only.
