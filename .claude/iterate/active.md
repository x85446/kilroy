# Iterate Task — routing-strategy ladder (role-split→fill-first) + claude -p gated-skip

Started: 2026-06-26T04:26Z (planned)
CWD: /Users/travis/workspace/x85446/kilroy
phase: executing
running: 2026-06-28T00:30:00Z

## Goal
Make routing a config-driven degradation ladder — role-split when both engines
are healthy, fill-first onto the survivor when one is gated (via pipeclean to the
non-gated engine), climbing back to role-split on recovery, pausing only when both
are gated — with round-robin and the single modes also selectable, and stop
`claude -p` from false-failing while claude is gated.

## Background (from this session)
- cliproxyapi `:8317` fronts Claude (`claude-*.json`) + Codex (`codex-*.json`).
  Pool alias `auto` (claude-opus-4-8 + gpt-5.5) via a self-referential
  `openai-compatibility` provider. kilroy reaches it through the anthropic
  adapter. Direct paths also work: `/v1/messages` model=claude-* (anthropic) and
  `/v1/chat/completions` model=gpt-5.5 (openai), both via the proxy.
- Gate (`kilroyHelp gate run`, one config `/etc/kilroy-usage-gate.conf`,
  `PROVIDERS=claude codex`) probes both (claude `/api/oauth/usage`, codex
  `backend-api/codex/usage`) and flips each auth's `.disabled` (hot-reloaded) as
  the failover lever. Pausing uses `launch stopsafe`; resuming `launch resume`.
- `kilroyHelp pipeclean` (creds) rewrites a graph's per-node `llm_provider`/
  `llm_model`. Today it maps everything to anthropic tiers; it is the natural
  place to express per-node engine assignment and to re-target a gated engine.
- `claude` IS installed; the prior "command not found" was a stripped
  non-interactive PATH. A direct claude-model call returns "unknown provider"
  while claude's auth is `disabled` by the gate, so `claude -p` false-FAILs when
  claude is the gated provider.

## Strategy ladder (the model this plan implements)
`STRATEGY` is an ordered, space-separated degradation ladder in the one gate
config. Each gate tick picks the FIRST mode satisfiable by the currently-healthy
provider set; when the active mode changes it stopsafe→re-pipeclean/rewire→resume.
- **role-split** — needs BOTH engines healthy: each node runs on its role's
  engine (strong/coding → PRIMARY engine, default/other → SECONDARY engine).
- **fill-first** — needs ≥1 healthy: all nodes run on the survivor (the
  non-gated engine); pipeclean re-targets the gated engine's nodes to it.
- **round-robin** — needs ≥1 healthy: nodes use the `auto` pool; the gate's
  auth-disable makes the pool serve whoever is healthy.
- No engine healthy → pause the run; resume/climb the ladder on recovery.
Default ladder: `role-split fill-first` (both healthy → role-split; one gated →
pipeclean to the survivor = fill-first; recovery → pipeclean return to
role-split; both gated → pause). Single values (`round-robin`, `fill-first`,
`role-split`) are also valid.

## Steps
1. Add to the single gate config `/etc/kilroy-usage-gate.conf` (+ defaults text +
   `_load_gate_config`): `STRATEGY` (ordered ladder, default `role-split
   fill-first`), `PRIMARY` (default claude), and a role→engine map
   (`ROLE_STRONG_PROVIDER`=PRIMARY, `ROLE_DEFAULT_PROVIDER`=SECONDARY). Re-read
   each tick; `kilroyHelp gate --show-config` prints them. `kilroyHelp run` reads
   the same file so one knob governs gate AND launch wiring.
2. Give `kilroyHelp pipeclean` two explicit modes used by the ladder: a
   role-split assignment (per-node engine by strong/default role → PRIMARY/
   SECONDARY) and a to-engine override (rewrite ALL nodes to one engine — the
   survivor). Both idempotent and re-runnable on a live run's graph.
3. Implement mode round-robin under the knob: kilroy targets the `auto` pool;
   the gate keeps every healthy provider in the pool and disables gated ones;
   pause only when all gated.
4. Implement mode fill-first under the knob: all nodes route to PRIMARY while it
   is healthy (the gate holds the secondary out / pipeclean targets PRIMARY); on
   PRIMARY gating, route all to the survivor; pause only when both gated.
5. Implement mode role-split + the ladder engine: with both healthy, ensure the
   run's graph is role-split-pipecleaned and both base-urls are wired so each
   role hits its engine. When the gate detects a provider transition it
   stopsafe→re-pipecleans→resumes: one engine gated → pipeclean the gated
   engine's nodes to the survivor (drop to fill-first); the gated engine recovers
   → pipeclean return to the role-split assignment (climb back); both gated →
   pause; track the active rung to avoid redundant rewrites.
6. Fix `_probe_claude_p`: locate the `claude` binary robustly (login-shell PATH +
   common install dirs) and SKIP with a "skipped (claude gated)" line (green, no
   action-needed hint) whenever the claude auth is gated/disabled by the gate —
   never report FAIL for a deliberately-gated provider.
7. Document the ladder + all modes + the gated-skip in
   `docs/kilroy-cliproxy-architecture.md` and `kilroyHelp gate`/`status` help.

## Validation
1. `kilroyHelp gate --show-config` prints `STRATEGY=role-split fill-first`
   (default), `PRIMARY=claude`, and the role→engine keys; editing `STRATEGY` to
   `round-robin` in `/etc/kilroy-usage-gate.conf` and re-running reflects it with
   no restart (no `systemctl restart cliproxyapi`).
2. `kilroyHelp pipeclean` role-split mode on a 2-node graph (one strong/coding
   node, one default node) yields one node `llm_provider`=PRIMARY-engine and one
   =SECONDARY-engine; to-engine mode on the same graph rewrites BOTH nodes to the
   named survivor engine. Verified by grepping the rewritten `.dot`.
3. STRATEGY=round-robin, both injected under budget (`GATE_TEST_5H=0
   GATE_TEST_CX_5H=0 kilroyHelp gate --tick`): both auths `disabled=false`; 8×
   `curl model=auto` on `:8317/v1/messages` returns a MIX of claude-opus-4-8 +
   gpt-5.5. One injected gated → 8× `auto` returns ALL survivor; both gated →
   `RUN: would PARK`.
4. STRATEGY=fill-first PRIMARY=claude, both under budget: 8× `auto` returns ALL
   claude-opus-4-8; PRIMARY gated → 8× `auto` returns ALL gpt-5.5; both gated →
   `gate --tick` → `RUN: would PARK`.
5. Default ladder (`STRATEGY=role-split fill-first`) on a real minimal 2-node run
   through the proxy: (a) both healthy → proxy journal correlated to the run shows
   the coding node served by the PRIMARY engine's model and the default node by
   the SECONDARY's (distinct upstreams); (b) inject PRIMARY gated → the gate
   stopsafe→pipecleans the run's graph to the survivor→resumes, and the next
   node.completed is served by the survivor engine (graph `.dot` now shows the
   re-targeted provider); (c) clear the gate → the gate pipecleans-return to the
   role-split assignment→resumes and the coding node is again served by PRIMARY;
   (d) both gated → run is stopsafe-paused. Each transition is logged in the gate
   log with the rung name.
6. `kilroyHelp status` while claude is gated (auth `disabled=true`, gate running):
   shows `claude -p: skipped (claude gated)` (dim, not red, no action-needed
   hint) and the run verdict is unaffected; with claude un-gated, `claude -p`
   actually executes (real reply or a genuine error) via the located binary.
7. `docs/kilroy-cliproxy-architecture.md` has a "routing strategies" section
   covering the ladder + three modes + the config knob; `kilroyHelp gate --help`
   lists `STRATEGY`, its ladder semantics, and the values.

## Constraints
- Context: `kilroyHelp`, the gate daemon, `/etc/kilroy-usage-gate.conf` are
  UNVERSIONED — edit in `~/workspace/x85446/creds/`, deploy by scp to
  `/opt/darkfactory/bin/` (verify checksums), NEVER commit to the kilroy repo.
- Context: kilroy code / pipeclean role-assignment / base-url wiring changes go
  in the kilroy repo per the Prime Directive; proxy + gate + launch operational
  changes go in creds.
- cli-proxy-api is actively serving — apply config/auth changes by hot-reload
  (file-watcher), NEVER `systemctl restart cliproxyapi`.
- ONE config file (`/etc/kilroy-usage-gate.conf`) drives everything: `STRATEGY`,
  `PRIMARY`, `PROVIDERS`, role map — read by both the gate and `kilroyHelp run`.
- Default `STRATEGY=role-split fill-first`. The ladder degrades on single-gate
  and climbs back on recovery; pause only when ALL engines are gated.
- Failover/return mechanism is pipeclean-driven (re-pipeclean the gated engine's
  nodes to the survivor; pipeclean-return on recovery) + stopsafe→resume so kilroy
  picks up the rewritten graph. If `resume` does not honor a rewritten graph, the
  executor finds the path that meets the validation (e.g. per-role pool aliases
  with failover order, or whatever makes the served engine actually change) —
  the contract is the observed served-engine behavior, not the literal command.
- `claude` IS installed — locate it (login-shell PATH + common dirs); only skip
  the probe when claude is *gated*, not when merely absent from a stripped PATH.
- OAuth tokens must NEVER be printed/echoed; probes read them into locals only.
- Restore both auths + the graph to their correct state after each injection test
  (leave no provider stranded `disabled` and no test graph mis-pipecleaned).
- Burnout stays opt-in (`BURNOUT_ARMED=1`/`MODE=burnout`); never self-arms.

## Progress
- [x] 1. config knob (STRATEGY ladder + PRIMARY + role map)
- [x] 2. pipeclean modes (role-split assignment + to-engine)
- [ ] 3. round-robin mode (pool + auth failover)
- [ ] 4. fill-first mode (all→PRIMARY then survivor)
- [ ] 5. role-split + ladder engine (pipeclean-driven failover/return)
- [ ] 6. claude -p gated-skip + robust locate
- [ ] 7. docs

## Design notes (resolved during execution)
- Two routing families: POOL-based (round-robin → kilroy model=auto + gate
  auth-disable failover) and GRAPH-based (role-split + fill-first → kilroy direct
  per-node providers; gate re-pipecleans graph + stopsafe→resume on transitions).
- Ladder default `role-split fill-first` is GRAPH-based: role-split when both
  healthy, to-survivor pipeclean (fill-first) when one gated, pipeclean-return on
  recovery, pause when both. round-robin is a standalone POOL-based mode.
- Engine→kilroy mapping: claude→(anthropic, claude-opus-4-8); codex→(openai, gpt-5.5).
- role-split run needs BOTH ANTHROPIC_BASE_URL + OPENAI_BASE_URL → proxy; pool
  modes need only ANTHROPIC_BASE_URL + --force-model anthropic=auto.

## Decisions log
- 2026-06-28T00:00Z entry: transitioned plan→executing, /loop armed (a1365b74).

## Status / Log
- 2026-06-28T00:00Z starting step 1 (config knob).
- 2026-06-28T00:10Z step1 DONE. Added STRATEGY (default "role-split fill-first"),
  PRIMARY, ROLE_STRONG/DEFAULT_PROVIDER to config defaults + loader + show-config +
  _engine_to_provider_model (claude→anthropic/claude-opus-4-8, codex→openai/
  gpt-5.5). Live config updated. Val1 GREEN (show-config + override no-restart).
- 2026-06-28T00:25Z step2 DONE. Real graphs use CSS model_stylesheet rules
  (`.hard { llm_model: opus; llm_provider: anthropic; }`), NOT DOT attrs — class =
  role, current model (opus/sonnet) = strong/default. Extended cmd_pipeclean with
  --mode role-split (strong→STRONG_ENGINE, default→DEFAULT_ENGINE) and to-engine
  (all→TARGET_ENGINE); back-compat default anthropic-tiers. Fixed
  _engine_to_provider_model to emit trailing newline (read-under-set-e bug). Val2
  GREEN: role-split → .hard=anthropic/opus-4-8, *=openai/gpt-5.5; to-engine→both gpt-5.5.

## DESIGN RESOLUTION for steps 3-5 (the unifying model)
- A run is ONE family, chosen by whether STRATEGY contains role-split:
  - **graph family** (STRATEGY has role-split): kilroy uses DIRECT per-node
    providers (both ANTHROPIC_BASE_URL + OPENAI_BASE_URL, no --force-model). The
    gate's lever is PIPECLEAN: role-split when both healthy, to-engine(survivor)
    when one gated; auths STAY ENABLED (so claude -p still works, recovery easy).
    Mode change → stopsafe→pipeclean→resume.
  - **pool family** (no role-split: round-robin and/or fill-first): kilroy uses
    model=auto (pool). The gate's lever is AUTH-DISABLE. round-robin=both healthy
    in pool; fill-first=keep PRIMARY-only in pool until gated then swap.
- Both families pause the run only when ALL providers gated; resume/climb on recovery.
- Refactor needed: split _gate_eval_provider into DECIDE-only (verdict, no
  enforce) + a strategy-aware ENFORCE dispatcher keyed on family+active-rung.
