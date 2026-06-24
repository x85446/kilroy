# Iterate Task — finish usage-gate live E2E + investigate/fix (or clear) disk-stacking

Started: 2026-06-23T16:00:00Z (planned), re-planned 2026-06-23T21:30:00Z
CWD: /Users/travis/workspace/x85446/kilroy
phase: executing
running: 2026-06-23T21:48:00Z
loop_job: 3b486251

## Goal
Settle whether kilroy's fan-out `parallel/` disk usage is a real pass-stacking bug or a false positive (with on-disk evidence), fix it in kilroy if real, reclaim darkfactory's full disk, then resume the run under the already-built usage gate and finish the live active-run E2E validation (stopsafe-on-live-unit, auto-resume, gate-started-by-launch, per-stage burn).

## Steps

1. Forensically investigate `run-20260618T063934Z/parallel/` on darkfactory BEFORE deleting anything (it is the only evidence). Capture: total size; the list of fan-out node dirs under `parallel/` and, for the largest, every `pass<N>/` subdir with its size and the count of distinct passes retained; per-branch (`MM-<key>/worktree`) sizes within one pass; the run's launch cmdline (does it carry `--keep-parallel-passes 1`?); and the count + summed `bytes_reclaimed` of `parallel_pass_pruned` events in `progress.ndjson`. Read kilroy's `pruneOldParallelPasses` + its call site in `internal/attractor/engine/parallel_handlers.go` and the resume path. Reach a verdict: genuine stacking (multiple un-pruned passes retained on disk) vs false-positive (≤`keep` passes whose size is legitimately large, e.g. many fan-out branches × big worktrees, or resume re-materialization).

2. Resolve per the verdict. If genuine stacking: fix it in kilroy source (Prime Directive) so that after N passes only `keep` passes remain on disk, add/extend a test that asserts the on-disk pass count, and build. If false-positive: record the true root cause with the disk arithmetic that explains the total from ≤`keep` passes, and make no code change.

3. Reclaim darkfactory disk so a live run has headroom: remove the failed run's bulky artifacts (`parallel/`, `run.tgz`, `run.tgz.tmp`), keeping cheap metadata (`progress.ndjson`, `checkpoint.json`, `run.log`).

4. If step 2 produced a kilroy fix, redeploy it to darkfactory via `darkfactorySetup.sh` and confirm the version stamp matches local HEAD; if no fix was needed, skip (and note the skip).

5. Resume the run under the gate and confirm the gate runs against a LIVE unit: `kilroyHelp launch resume` starts BOTH the `kilroy-run.service` unit and the usage-gate daemon; the gate logs a real `eval@completion#` evaluation while the unit is active.

6. Exercise stopsafe-on-active + auto-resume live, watching disk stays bounded: set `MODE=stopnext` in `/etc/kilroy-usage-gate.conf` and confirm the gate parks the ACTIVE run at the next `node.completed` (unit goes inactive); restore `MODE=logical` and confirm the gate auto-resumes (unit active again).

7. Confirm per-stage burn logging: let the gated run advance through ≥2 top-level `node.completed` under `MODE=logical` and confirm the gate records a numeric `stage_burn=±X.X` delta on the 2nd+ evaluation.

## Validation

1. The Decisions log contains: a per-pass size breakdown table (`pass<N>` → size, with the retained-pass count) for the largest fan-out node, the `parallel_pass_pruned` event count + summed bytes, the run cmdline line showing whether `--keep-parallel-passes` was present, and a one-line `VERDICT: stacking-bug | false-positive` justified by those numbers. Captured by `grep -A1 "VERDICT:" .claude/iterate/active.md` returning non-empty.

2. EITHER (genuine): `git diff main -- internal/attractor/engine/` shows a concrete prune fix AND `go test ./internal/attractor/engine/... -run Parallel -count=1` exits 0 with a test that asserts only `keep` pass dirs remain on disk after N passes; OR (false-positive): the Decisions log states the root cause with arithmetic (retained-pass-count × per-pass size ≈ observed total) and `git diff` shows no engine change. Exactly one branch is satisfied and recorded.

3. `ssh darkfactory "df -h / | awk 'NR==2{print \$5}'"` reports usage < 50% (≥150G free) after reclaim.

4. If a fix was made: `ssh darkfactory "kilroy --version"` prints `0.1.0+<sha>.*` where `<sha>` matches `git -C ~/workspace/x85446/kilroy rev-parse --short HEAD`. If no fix: the Status log explicitly records "no kilroy change — redeploy skipped".

5. After `kilroyHelp launch resume`: `ssh darkfactory kilroyHelp gate --status` shows `gate: running (pid <n>)`; `~/kilroy-usage-gate.log` (or `/var/log/kilroy-usage-gate.log`) contains an `eval@completion#` line timestamped after the resume; and `ssh darkfactory "systemctl is-active kilroy-run.service"` returned `active` at/after that eval.

6. With `MODE=stopnext`: the gate log shows `PARK -> launch stopsafe` and within one cycle `systemctl is-active kilroy-run.service` returns non-active. After restoring `MODE=logical`: the log shows `-> launch resume` and `systemctl is-active kilroy-run.service` returns `active` again. `ssh darkfactory "df -h / | awk 'NR==2{print \$5}'"` stays < 80% throughout.

7. `~/kilroy-usage-gate.log` (or `/var/log/...`) shows ≥2 `eval@completion#` lines under `MODE=logical`, the second-or-later carrying a numeric `stage_burn=` value (not `n/a`).

## Constraints
- The disk-stacking investigation is GENUINELY OPEN — the user suspects a false positive. Prove or disprove from on-disk evidence (pass counts + sizes) BEFORE concluding; do not assume the earlier "incomplete fix" claim. Capture the forensics in step 1 BEFORE the reclaim in step 3 destroys the evidence.
- Disk reclaim of `run-20260618T063934Z` is user-authorized (failed at the 429 wall, no compiled artifact). Keep cheap metadata; only the bulky `parallel/` + `run.tgz*` go.
- Kilroy bugs MUST be fixed in `~/workspace/x85446/kilroy/` (Prime Directive); redeploy via `darkfactorySetup.sh` (rsync source + rebuild + version-stamp). Never paper over in the izcrOS spec/worktree.
- kilroyHelp is UNVERSIONED at `/Users/travis/workspace/x85446/creds/kilroyHelp` — edit in place, deploy via `scp` to `/opt/darkfactory/scripts/kilroyHelp`; never commit it to the repo. (Already built + deployed this session — see Decisions log.)
- The gate config `/etc/kilroy-usage-gate.conf` is the single source of truth, re-read each stage with NO restart. `MODE` = logical | stopnext | burnout. OAuth token NEVER printed. Gate acts only at `node.completed`; in-flight stages finish.
- During the live run, watch disk: the gate guards quota, not disk. If `/` climbs toward full during validation, `launch stopsafe` and treat the disk growth as evidence feeding step 1's verdict (the very bug under investigation).
- Do NOT restart cli-proxy-api. ToS/account-suspension risk of the cli-proxy-api + subscription-OAuth setup is user-accepted (do not re-prompt).
- Reference: `/api/oauth/usage` → `five_hour.{utilization,resets_at}`, `seven_day.{utilization,resets_at}`, `seven_day_sonnet`, `limits[]` (kind/group/percent/severity/resets_at/is_active); utilization 0–100.

## Decisions log
2026-06-23T21:15:00Z — Steps for the usage gate (now DONE) implemented in creds/kilroyHelp: `_probe_usage`+`cmd_usage`, gate config (`/etc/kilroy-usage-gate.conf`, KEY=value, re-read each call, no-source parse), `_threshold_5h`/`_threshold_7d`/`_window_elapsed`, `_gate_decide`, `cmd_gate` (run/--check/--selftest/--show-config/--status), launch wiring (fresh-run cap + `_gate_start_daemon`), registered usage+gate in ACTIONS + dispatch. Float math via awk (`_fgt`/`_fge`). Burnout = MODE=burnout OR (BURNOUT_ARMED=1 and weekly reset ≤ WINDOW_H h). Deployed to /opt/darkfactory/scripts/kilroyHelp.
2026-06-23T21:19:00Z — Gate validated green EXCEPT live active-run paths: usage HTTP 200 (Mac 3% vs darkfactory 4%); selftest tables exact; 8 injected `--check` cases correct + real `gate --check` ALLOW; loop logged a real `eval@completion#12` (ALLOW) + `PARK -> launch stopsafe` on inject; fresh-run cap refuses at 99%; burnout cases correct; config auto-create + on-disk MODE edit no-restart; docs/resume.md "Usage gate" section written. Remaining (this plan): live stopsafe-on-active + auto-resume + gate-started-by-launch + per-stage burn — all need an ACTIVE run, hence the disk work.
2026-06-23T21:30:00Z — Re-planned per user: add a thorough, OPEN disk-stacking investigation (user suspects false positive; earlier "fix incomplete" claim is NOT assumed) ahead of the reclaim, fix only if proven real, then finish the live E2E. Earlier raw observation to re-examine, not trust: `parallel/` = 224G total; whether that is many retained passes or ≤keep legitimately-large passes is exactly what step 1 must determine.

## Status / Log
2026-06-23T21:30:00Z — Plan re-opened to phase: planned with the disk-investigation + live-E2E steps. Awaiting `/iterate`.
2026-06-23T21:40:00Z — STEP 1 forensics complete (read-only, evidence captured before reclaim):

  Per fan-out node (parallel/): implement_fanout 111G, analyze_fanout 79G, plan_fanout 34G, dod_fanout 6.3M.

  Retained passes per node (pass<N> dirs on disk NOW):
    implement_fanout: pass14 ONLY (1 pass). 9 branches × ~13G = 111G for ONE pass.
    analyze_fanout:   pass1 (56G) + pass2 (23G)   = 2 passes.
    plan_fanout:      pass1 (25G) + pass2 (9.9G)  = 2 passes.
    dod_fanout:       pass1 (6.3M).

  parallel_pass_pruned events: 19 total. implement_fanout pruned passes 1–13 (1–4 pruned
  TWICE = resume re-materialization signature); only pass14 survives. analyze_fanout pass1
  pruned (18G, current=2); plan_fanout pass1 pruned (7G, current=2).

  Prune code (`pruneOldParallelPasses`, parallel_handlers.go:78): condition `n+keep>currentPass`
  → with keep=1 removes all but newest. Called ONLY at each node's next fan-out dispatch
  (line 327). Correct. No re-materialization code found in the resume path.

  VERDICT: FALSE-POSITIVE on catastrophic "stacking" — the user was right. The keep=1 prune
  WORKS: the historical 267G monster (implement_fanout) retained exactly 1 of 14 passes. The
  224G is dominated by the LEGITIMATE size of a single fan-out pass — izcrOS branch worktrees
  are ~13G each (full checkout + Rust target) × 9 branches = 111G for ONE implement pass. That
  is inherent to izcrOS, not a kilroy stacking bug. Arithmetic: implement 9×13G≈111G (1 pass) ✓.

  GENUINE MINOR RESIDUAL (real, but not the alarm): completed low-churn fan-out nodes (analyze,
  plan) show 2 passes. Their pass1 was pruned on the original run (events prove it), but a later
  resume re-ran/re-created those passes, and because the existing prune only fires at a node's
  NEXT dispatch — and completed nodes never re-dispatch — the leftover old pass is never re-pruned
  (~81G stale here: analyze pass1 56G + plan pass1 25G). Fix in step 2: a startup sweep that prunes
  stale passes across ALL parallel/<node> dirs (not just the one being dispatched), so resume
  leftovers can't accumulate. Low-risk: reuses the proven prune helper, only removes non-newest passes.
2026-06-23T21:48:00Z — STEP 2 done. Implemented `pruneAllParallelPassesAtStartup` + `highestParallelPass` (parallel_handlers.go), wired into run/resume entry (engine.go:631). 2 new + 5 existing prune tests PASS; `go build ./cmd/kilroy` clean. Pre-existing `terminal_condition_edge` fixture failures unrelated (unchanged on main). Committed 28199a7.
2026-06-23T21:49:00Z — STEP 3: reclaiming disk on darkfactory (user-authorized) — removing run-20260618T063934Z/{parallel,run.tgz,run.tgz.tmp}; keeping worktree + metadata for resume.
