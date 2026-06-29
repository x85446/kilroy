# Iterate Task — kilroy deterministic-cycle escalation ladder (levers #1+#2)

Started: 2026-06-29T07:01:10Z (planned)
CWD: /Users/travis/workspace/x85446/kilroy
phase: planned
running: false

## Goal
Make kilroy's deterministic-failure-cycle breaker stop retrying verbatim: keep counts 1–5 unchanged, then from count 6 apply two domain-agnostic escalation levers (inject failure evidence into the node prompt, escalate that node to a different engine), aborting only at count 10 — then build, deploy, run the izcrOS run on it, and monitor that the ladder fires and changes behavior.

## Steps
1. In the kilroy repo, add the ladder config + dispatch to the deterministic-failure-cycle path (`internal/attractor/engine/engine.go` ~846–885 and `internal/attractor/engine/loop_restart_policy.go`): add a graph attr `loop_restart_ladder_start` (parsed like `loop_restart_signature_limit`, default 0 = disabled → today's behavior). When `ladder_start>0 && ladder_start <= count < limit`, instead of plain retry-or-abort, mark per-node escalation state for the next attempt and emit a `deterministic_failure_cycle_ladder` progress event naming the levers fired. Counts `< ladder_start` behave exactly as today; abort still only at `count >= limit`.
2. Implement lever #1 — evidence injection: on a laddered attempt, the failing node's next prompt/context includes the prior attempt's failure output + diff, sourced from the failure dossier the engine already maintains (`updateFailureDossierContext` / `failure_dossier_updated`). Surface it into the node's agent input so the model sees "you already tried this; it failed identically; here is the exact error + your last diff".
3. Implement lever #2 — engine escalation: on a laddered attempt, override that node's resolved provider to the configured alternate engine (reuse the dual-AI cliproxy plumbing — claude↔codex/opus) via a runtime per-node provider override the AgentRouter honors, so the laddered attempt's `provider_selected` names a different engine than the pre-ladder attempts.
4. Build kilroy from the modified source and install it on darkfactory (`kilroyHelp build-install`), keeping `--no-stage-archive-stacking` present; then set the izcrOS run's resumable graph attrs to `loop_restart_signature_limit=10` and `loop_restart_ladder_start=6`.
5. Resume the izcrOS run under the new binary + new attrs (`kilroyHelp launch resume`) so the recurring verify_test/verify_fmt deterministic signatures now drive the ladder rather than aborting at 5.
6. Monitor the live run: confirm that when a signature reaches 6 the ladder fires (both levers), the node re-attempts on the alternate engine with the failure evidence, and the run advances strictly past the previous count-5 abort point.
7. If anything breaks (build error, panic, ladder doesn't fire, resume fails, run stalls), diagnose and fix in the kilroy source (Prime Directive — fix kilroy, never the izcrOS target), rebuild, redeploy, re-resume until the ladder fires correctly and the run is progressing.

## Validation
1. `cd /Users/travis/workspace/x85446/kilroy && go build ./...` exits 0, AND an extended deterministic-cycle unit test (build on `reliability_helpers_test.go`'s `deterministicCycleFixtureHandler`) with `ladder_start=6, limit=10` proves: no abort at count 5; a `deterministic_failure_cycle_ladder` event fires at counts 6–9; abort happens at exactly count 10. `go test ./internal/attractor/engine/ -run 'Ladder' -count=1` passes.
2. A unit/integration test asserts that on a laddered attempt the agent input handed to the provider contains the injected failure-evidence text (assert the dossier/last-error string is present in the prompt/context). `go test ./internal/attractor/engine/ -run 'LadderEvidence' -count=1` passes.
3. A unit/integration test asserts that on a laddered attempt the `provider_selected` (or resolved provider) for the stuck node differs from the provider used on attempts 1–5. `go test ./internal/attractor/engine/ -run 'LadderEngine' -count=1` passes.
4. On darkfactory: `kilroy version` (or build timestamp) reflects the new binary, `kilroyHelp build-install` exited 0 with the `--no-stage-archive-stacking` flag confirmed, AND the izcrOS run's active graph reports both attrs — `grep -E 'loop_restart_signature_limit|loop_restart_ladder_start' <run logs-root>/worktree/<graph>.dot` (or the resumable graph) shows `=10` and `=6`.
5. `kilroyHelp launch resume` brings the run up: `kilroyHelp status` shows `unit: active` (or progressing) and `progress.ndjson` gains fresh events within 2 min; grepping `progress.ndjson` shows NO `deterministic_failure_cycle_breaker` at `signature_count=5` after this resume.
6. In the live `progress.ndjson`, for at least one recurring signature: a `deterministic_failure_cycle_ladder` event at `signature_count>=6` listing both levers, followed by a `provider_selected` on a different engine for that node, and the node re-running (not an immediate abort). The run reaches a state strictly beyond the old wall — a new node transition past `verify_test`/`verify_fmt`, OR a materially different/advanced failure signature — proving the loop moved rather than thrashing.
7. After each fix cycle the previously-failing check among 1–6 now passes, and on darkfactory a `kilroy attractor` process is alive and `progress.ndjson`'s last write is < 5 min old (run actively progressing).

## Constraints
- Prime Directive: ALL changes for the kilroy weakness go in the kilroy repo (`/Users/travis/workspace/x85446/kilroy`, built to the darkfactory binary). NEVER fix this by editing the izcrOS worktree or spec.
- Domain-agnostic only: kilroy must not learn kernel/PCI/izcrOS specifics. The ladder changes only *how* a node is retried (evidence + engine), never *what* kilroy knows about the target.
- Do NOT pre-fix the izcrOS `CONFIG_PCI` spec gap. The laddered run is the live test of whether the attractor can now self-converge; leaving the spec bug in place is intentional. If levers #1+#2 prove insufficient for full izcrOS convergence, report that as data — the task's contract is the ladder firing + behavior changing (validations 1–6), not izcrOS reaching green.
- Engine escalation must reuse the existing dual-AI cliproxy plumbing; keep BOTH engines reachable during the test (don't let the gate disable one). Don't restart cli-proxy-api — hot-reload only.
- The izcrOS run is ~1.25 h per cycle and the ladder only engages after 6 recurrences of a signature, so monitoring may span hours — keep the loop alive; "still running" is not "stuck".
- `creds/kilroyHelp` + gate config stay unversioned — edit in place, scp-deploy to `/opt/darkfactory/bin/`, never commit. kilroy source changes are committed only if the user explicitly asks.
- Context: this builds on levers #1 (inject evidence) and #2 (escalate engine) from the prior discussion; levers #3–#5 (sampling perturbation, backtrack-upstream, diagnose node) are explicitly out of scope for this task.

## Decisions log
(empty until execution)

## Status / Log
(empty until execution)
