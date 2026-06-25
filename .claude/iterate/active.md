# Iterate Task — dual-AI gate control (claude + codex) through cliproxyapi

Started: 2026-06-25T18:15:34Z (planned)
CWD: /Users/travis/workspace/x85446/kilroy
phase: executing
running: 2026-06-25T19:55:00Z

## Goal
Make kilroy drive both Claude (opus-4.8) and OpenAI (gpt-5.5) through cliproxyapi
as a single round-robin pool that fails over when one side is gate-checked and
only pauses the run when both are gated — with one shared gate config governing
both providers.

## Architecture (current state, established by investigation)
- **Today it is ONE path, Anthropic-only.** `kilroy-run.service` exports only
  `ANTHROPIC_BASE_URL=http://127.0.0.1:8317`. kilroy's anthropic adapter →
  cliproxyapi → `claude-*.json` auth. The `codex-*.json` auth now exists in the
  proxy but kilroy never calls it (no `OPENAI_BASE_URL`, no openai-model nodes).
- kilroy is provider-aware by design: per-node `llm_provider`/`llm_model`
  (DOT `model_stylesheet` + node attrs), overridable by `--force-model
  provider=model`, recorded in `manifest.json:force_models`. anthropic and
  openai are separate adapters with separate base-url env vars and wire protocols
  (`internal/llm/providers/{anthropic,openai}/adapter.go`,
  `internal/attractor/engine/agent_router.go`).
- cliproxyapi (port 8317, `routing.strategy: round-robin`) routes by model name
  and round-robins only among credentials serving the *same* model. It has a
  protocol-translation layer, `model-aliases`, `quota-exceeded` auto-switch, and
  per-auth `disabled`/cooldown. Proxy exposes `claude-opus-4-8` and `gpt-5.5`.
- **Target (Mission 1A):** one endpoint + one alias pools `claude-opus-4-8` +
  `gpt-5.5`; the gate disables one upstream on gate-check (proxy fails over),
  pausing the run only when both are disabled. 1B (kilroy agent-aware, two base
  URLs + per-node models) is the accepted fallback only if cross-provider pooling
  proves impossible.

## Steps
1. Write `docs/kilroy-cliproxy-architecture.md` capturing the kilroy↔cliproxyapi
   wiring and the current single-vs-dual path reality (derived from the live
   `kilroy-run.service` env + cliproxyapi.conf + the adapter/agent_router code).
2. Build and prove a single-endpoint cross-provider round-robin pool in
   cliproxyapi: one model alias (e.g. `auto`) that pools `claude-opus-4-8` +
   `gpt-5.5` across the two auths on the one `:8317` endpoint, with the proxy
   translating so a single client protocol gets responses from both upstreams.
3. Establish an OpenAI/codex gate signal in kilroyHelp: a probe that returns
   codex gate-state (a real usage/rate endpoint for the ChatGPT/codex OAuth if
   one exists; otherwise derived from the proxy's 429/quota-cooldown state),
   parallel to the existing Anthropic `/api/oauth/usage` probe.
4. Generalize the usage-gate to per-provider control driven by ONE
   `/etc/kilroy-usage-gate.conf` (Mission 3): each tick evaluates both providers;
   a gated provider is enforced by setting `disabled:true` on its cliproxyapi
   auth (proxy fails over), restored to `disabled:false` when its window clears;
   the run is `launch stopsafe`-paused only when BOTH are gated and resumed when
   either frees.
5. Point kilroy at the pooled endpoint and record the target models (Missions
   1A + 2): wire the launch env / graph `model_stylesheet` / `--force-model` so a
   run uses the pooled alias and records `claude-opus-4-8` + `gpt-5.5`; the run
   actually completes nodes served alternately by both upstreams.
6. Extend `kilroyHelp status` to show both providers' gate-state and which
   upstream is currently serving (builds on the per-provider auth view already
   added).

## Validation
1. `docs/kilroy-cliproxy-architecture.md` exists and its "current path" section
   matches the live system: `grep -c ANTHROPIC_BASE_URL` of the captured service
   env is 1 and `OPENAI_BASE_URL` is absent (or, post-step-5, both are present);
   the doc names the adapter files and the round-robin routing fact.
2. From darkfactory: `for i in $(seq 1 10); do curl -s http://127.0.0.1:8317/v1/messages
   -H 'authorization: Bearer dummy-key' -H 'content-type: application/json'
   -d '{"model":"auto","max_tokens":16,"messages":[{"role":"user","content":"ping"}]}'
   ; done` returns HTTP 200 bodies, AND the proxy journal/logs over those 10
   calls show BOTH `claude-*.json` and `codex-*.json` selected (alternation
   proves the cross-provider pool round-robins on one endpoint).
3. `kilroyHelp usage --provider codex` (or `kilroyHelp gate --status` extended)
   prints a codex gate-state line (utilization % or cooldown state) with exit 0
   and NO token in the output (`grep -iE 'sk-|access_token|Bearer [A-Za-z0-9]'`
   of the output is empty).
4. With a test injection that gates only Claude: one gate tick leaves
   `claude-*.json` with `disabled:true`, `codex-*.json` with `disabled:false`,
   `systemctl is-active kilroy-run.service` still `active`, and a live curl to
   the `auto` alias still returns 200 (served by codex). Injecting BOTH gated →
   next tick → `kilroy-run.service` is stopped via stopsafe. Clearing the Claude
   gate → next tick → `claude-*.json` back to `disabled:false` AND the run is
   resumed (`launch status` shows running). `grep -E 'claude|codex' /etc/kilroy-usage-gate.conf`
   shows both providers governed by the single file.
5. After wiring: a started/resumed run's `manifest.json` `force_models` contains
   `claude-opus-4-8` and `gpt-5.5` (or the `model_stylesheet` resolves to them),
   AND `run.log` shows ≥1 successful `node.completed` whose served model is a
   gpt-5.* upstream and ≥1 whose served model is claude-opus-4-8 (proving both
   AIs actually did work in the run).
6. `kilroyHelp status` output contains a per-provider gate block: one line for
   claude and one for codex, each showing active/gated state, and a line naming
   the currently-serving upstream — verified by running it live on darkfactory
   while one provider is test-gated.

## Constraints
- Context: `kilroyHelp`, the gate daemon, and `/etc/kilroy-usage-gate.conf` live
  in `/Users/travis/workspace/x85446/creds/` which is UNVERSIONED — edit in place,
  deploy via `scp` to `/opt/darkfactory/bin/` (verify checksums), NEVER commit to
  the kilroy repo.
- Context: kilroy code, model selection, base-url, and DOT `model_stylesheet`
  changes go in the kilroy repo per the Prime Directive (AGENTS.md). Proxy + gate
  operational changes go in creds. Do not paper over a kilroy bug in the proxy.
- cli-proxy-api (PID serving on :8317) is actively serving the live run. Prefer
  hot-reload of auth files (verify the proxy picks up `disabled` changes without
  a hard restart — that hot-reload IS the failover mechanism); if a reload is
  needed use the least-disruptive path and never drop the in-flight run's calls
  without cause.
- OAuth access/refresh/id tokens and account secrets must NEVER be printed,
  echoed, or logged. Probes read tokens into locals and discard them.
- Models are fixed: anthropic = `claude-opus-4-8`, openai = `gpt-5.5` (both
  confirmed present in the proxy `/v1/models` list).
- Target is Mission 1A (single endpoint, proxy round-robins, gate failover).
  Mission 1B (kilroy agent-aware: two base URLs + per-node models) is the
  user-accepted fallback ONLY after the pool approach in step 2 is genuinely
  exhausted — establish 1A first.
- Burnout stays opt-in (`BURNOUT_ARMED=1` / `MODE=burnout`); the gate never
  self-arms it. Per-provider gate logic must preserve this.
- Destructive/irreversible action on the shared darkfactory host requires the
  user to name the target; toggling cli-proxy-api privileged/auth state for
  failover is in-scope (it's reversible), full host changes are not.

## Progress
- [x] 1. architecture doc
- [x] 2. cross-provider round-robin pool (Mission 1A core)
- [x] 3. codex/openai gate signal
- [ ] 4. per-provider gate control + single config (Mission 3)
- [ ] 5. point kilroy at pool + record opus-4.8/gpt-5.5 (Missions 1A+2)
- [ ] 6. status shows both gate states

## Decisions log
- 2026-06-25T18:17Z step1: wrote docs/kilroy-cliproxy-architecture.md describing
  the current single-path (anthropic-only) wiring + the 1A-vs-1B mechanics.
  Chose to document current state now; will update the "path today" section in
  step 5 once kilroy points at the pool.

## Status / Log
- 2026-06-25T18:17Z step1 DONE. Validation 1b green: live `kilroy-run.service`
  env shows only ANTHROPIC_BASE_URL=http://127.0.0.1:8317 (no OPENAI_BASE_URL);
  doc names both adapter.go files + the round-robin routing fact. Next: step 2.
- 2026-06-25T19:30Z step2: probed protocol×upstream matrix on 8317 — all 4
  combos (anthropic/openai × claude/gpt) return 200, proxy translation is
  bidirectional. Tried oauth-model-alias overlap (alias both to "auto") on a
  throwaway 8319 instance: FAILED — ambiguous resolution (auto→claude-fable-5,
  →codex-auto-review, →literal auto). Pivoted to the documented mechanism: a
  self-referential `openai-compatibility` provider "dualpool" (base-url
  127.0.0.1:8317/v1, api-key dummy-key) with repeated alias auto→{claude-opus-4-8,
  gpt-5.5}. On 8319 test: clean 6/6 round-robin on BOTH protocols.
- 2026-06-25T19:41Z step2 DONE. Applied dualpool to live /etc/cliproxyapi.conf
  (backup .bak-20260625T194129Z); proxy file-watcher HOT-RELOADED (no restart,
  honoring the don't-restart constraint) — now "1 OpenAI-compat". Validation 2b
  GREEN on live 8317 /v1/messages: 10/10 HTTP 200, alternating
  claude-opus-4-8/gpt-5.5, 6/6 split. Test instance 8319 torn down. The auto
  alias is the single endpoint kilroy will target in step 5.
- 2026-06-25T19:54Z step3 DONE. Discovered the codex usage endpoint:
  https://chatgpt.com/backend-api/codex/usage returns rate_limit.primary_window
  (5h, window_s=18000) + secondary_window (7d, window_s=604800) as used_percent
  (0-100) + reset_after_seconds — a clean mirror of Anthropic's 5h/7d. Added
  KILROYHELP_CODEX_USAGE_URL/_AUTH_GLOB env defaults, _probe_usage_codex (CX_*
  vars, GATE_TEST_CX_* injection, token never printed, needs chatgpt-account-id
  +originator+user-agent headers), _usage_print_block helper, and extended
  cmd_usage with --provider claude|codex|both. Deployed (md5 0c118fc7). Validation
  3b GREEN: `usage --provider codex` exit 0, shows 5h/7d, no token leak. NOTE:
  live claude 7d=92% (>90% ceiling → why run is parked), codex 0% — the exact
  failover case step 4 handles. Same burn-envelope thresholds apply to both.
