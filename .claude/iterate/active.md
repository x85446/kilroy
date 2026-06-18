# Iterate Task — fix fan-out worktree stacking, drive izcrOS run to compiled

Started: 2026-06-18T05:10:00Z (planned), executing from 2026-06-18T05:15:00Z
CWD: /Users/travis/workspace/x85446/kilroy
phase: executing
running: 2026-06-18T07:28:30Z
planner: iterate-planner
loop_job: 3ee70191

## Goal
Fix the kilroy engine bug that retains every prior fan-out pass's worktrees
indefinitely (caused 267G disk exhaustion in run-20260617T211800Z); rebuild
and deploy the fixed kilroy to darkfactory; run a fresh kilroy session
against izcrOS all the way through to a successfully compiled artifact,
fixing whatever new kilroy bugs surface on darkfactory along the way.

## Steps

1. Investigate the parallel fan-out lifecycle in `internal/attractor/engine/parallel_handlers.go` and adjacent files. Locate (a) where each pass's `parallel/<node>/passN/MM-<key>/worktree/` directory is created (line 302/303), (b) where pass results are joined into the main worktree, (c) any existing flag or option that gates worktree retention. Write design notes inline in the Decisions log: chosen cleanup point, default retention policy (default: keep most-recent 1 pass), new CLI flag name (`--keep-parallel-passes N`).

2. Implement the fix in kilroy source. Add a `cleanupOldParallelPasses(logsRoot, parallelNodeID, currentPass, keep, gitOps, repoPath)` helper that: enumerates `logsRoot/parallel/<id>/pass*/`, identifies passes with number ≤ currentPass-keep, for each child worktree calls `git worktree remove --force <path>` (so the git registration is cleaned up) then `os.RemoveAll` on the pass dir, and emits a `parallel_pass_pruned` progress event with bytes-reclaimed. Wire it into the parallel handler immediately after the `runBranches` loop completes successfully AND the join node has consumed `parallel.results`. Add the `--keep-parallel-passes <n>` CLI flag to both `attractor run` and `attractor resume`, default 1. Pipe it through `RunOptions` similar to `NoStageArchiveStacking`. Update the usage strings in `cmd/kilroy/main.go`.

3. Add Go tests in `internal/attractor/engine/parallel_test.go`: (a) a fan-out node re-entered twice — after pass2 completes, `parallel/<id>/pass1/` no longer exists on disk and `git worktree list` does not contain pass1 paths; (b) with `--keep-parallel-passes 2`, both pass1 and pass2 remain after pass3 completes; (c) the `parallel_pass_pruned` progress event is appended with non-zero `bytes_reclaimed`.

4. Build kilroy locally and verify the new flag is present in `kilroy attractor run --help` and `kilroy attractor resume --help`. Run `go test ./internal/attractor/engine/... -run Parallel -count=1` and confirm green.

5. Commit the fix on a feature branch (`bug-fanout-pass-cleanup` or similar), push to `x85446/kilroy`, then merge to `main` (or directly commit to main per the repo's recent pattern of squashed merges — the choice is procedural, not a blocker). Update `kilroyHelp.cmd_build_install`'s flag-presence verification (lines ~825–838) so it also checks for `--keep-parallel-passes` on both `attractor run` and `attractor resume`. Save changes to `/Users/travis/workspace/x85446/creds/kilroyHelp` and `/Users/travis/workspace/x85446/creds/darkfactorySetup.sh` if any setup logic needs updating to surface the new flag.

6. Deploy to darkfactory by running `./darkfactorySetup.sh` from the workstation. The script's `check_kilroy_source` / `check_kilroy_binary` will detect the SHA bump, rsync the new source, and rebuild kilroy in place. Verify `ssh darkfactory kilroy --version` shows a build metadata sha matching current `git rev-parse --short HEAD` in `~/workspace/x85446/kilroy/`.

7. Launch a fresh kilroy run on darkfactory: `ssh darkfactory "cd ~/work/izcrOS && kilroyHelp launch run"`. Confirm via `kilroyHelp status` that the new run is active and that the journal banner does NOT say "lacks --no-stage-archive-stacking" or any new "lacks --keep-parallel-passes" warning.

8. Monitor the run end-to-end. Poll `kilroyHelp status` + `df -h /` on darkfactory at most every 5 minutes. For each node failure or kilroy bug that surfaces: triage per the CLAUDE.md rule (kilroy bug → fix in this repo and redeploy via step 6; spec bug → log and move on, since the goal is the kilroy run itself, not izcrOS correctness). Resume the run after each kilroy fix via `kilroyHelp launch resume`. Keep iterating until either (a) `run_completed:success` lands in progress.ndjson, or (b) the run reaches the build/compile node and that node produces compiled artifacts in `worktree/bin/`, `worktree/target/`, or whatever izcrOS's spec defines as the build output location.

9. Confirm compiled output. SSH to darkfactory, cd into the latest run's worktree (`cd "$(kilroyHelp results cdlatest)"`), enumerate the build output directory, and verify at least one ELF binary or disk image exists. Run `file <artifact>` on the largest output to confirm it is a compiled artifact (ELF, raw disk image, ISO, or fitting izcrOS's deliverable shape).

## Validation

1. The Decisions log contains a written design note covering: cleanup hook location (file:line), default retention (1), flag name (`--keep-parallel-passes`), and the rationale for choosing post-merge over pre-pass timing. Captured by running `grep -A 3 "design note" .claude/iterate/active.md` and seeing non-empty output.

2. `git diff main -- internal/attractor/engine/ cmd/kilroy/main.go` shows: (a) a new `cleanupOldParallelPasses` (or equivalent name) function, (b) a call site that invokes it after a successful pass, (c) plumbing for the new flag through `cmd/kilroy/main.go`'s flag parser and `RunOptions`, (d) usage-string updates listing the flag on both `attractor run` and `attractor resume`. Build succeeds: `go build ./cmd/kilroy` exit 0.

3. `go test ./internal/attractor/engine/... -run Parallel -count=1 -v` exits 0 with three new test functions visibly passing (names containing "Cleanup", "Keep", or "PassPruned"). Captured: paste the PASS lines into Decisions log.

4. `./bin/kilroy attractor run --help 2>&1 | grep -E "keep-parallel-passes"` returns a match and likewise for `./bin/kilroy attractor resume --help`. `./bin/kilroy --version` prints a build-metadata sha matching `git rev-parse --short HEAD`.

5. The feature branch is pushed and merged. `git log main --oneline -5` shows the fix commit. The verification in `cmd_build_install` now checks both flags. `kilroyHelp` is the source of truth for the verification — confirmed by re-reading `/Users/travis/workspace/x85446/creds/kilroyHelp` lines ~820–840.

6. On darkfactory: `ssh darkfactory "kilroy --version"` prints `0.1.0+<new-sha>.main` where `<new-sha>` matches `git -C ~/workspace/x85446/kilroy rev-parse --short HEAD`. `ssh darkfactory "kilroy attractor run --help 2>&1 | grep keep-parallel"` returns a match.

7. `ssh darkfactory kilroyHelp status` reports `status: active` (or `transitioning`), with a `run` field that is NOT `run-20260617T211800Z`. `ssh darkfactory "sudo journalctl -u kilroy-run.service --no-pager -n 30 | grep -i lacks"` returns no matches.

8. After ≥10 minutes of run progress, `ssh darkfactory "df -h / | awk 'NR==2{print \$5}'"` reports usage under 80%. The progress.ndjson shows at least one completed `implement_fanout` followed by a `parallel_pass_pruned` event (verifies the new cleanup is actually firing in production). Each new kilroy bug surfaced is fixed by editing this repo, redeployed via step 6, and the resume continues. The Decisions log records every distinct kilroy bug found+fixed during this iteration.

9. `ssh darkfactory "cd \"\$(kilroyHelp results cdlatest)\" && find worktree -maxdepth 4 -type f \\( -name '*.elf' -o -name 'vmlinuz*' -o -name '*.img' -o -name '*.iso' -o -path '*/bin/*' -o -path '*/target/release/*' \\) 2>/dev/null | head -20"` returns one or more matches. `file` on at least one of those outputs shows a binary-formatted file (ELF, "DOS/MBR boot sector", "ISO 9660", "data" with a non-trivial size, etc.). Final disk usage on darkfactory under 85%.

## Constraints

- Kilroy bugs MUST be fixed in `~/workspace/x85446/kilroy/` (this repo). Never paper over a kilroy bug by editing files inside the izcrOS spec or the produced worktree. (CLAUDE.md prime directive.)
- After every kilroy source change, redeploy via `./darkfactorySetup.sh` from the workstation — this is the single canonical path that rsyncs source + rebuilds + verifies version stamp on darkfactory.
- Disk on darkfactory is 291G (`/dev/sda2`). Throughout the run, treat 80% usage as a hard warning threshold and 90% as a hard fail-fast — if disk crosses 90% during the validating run, classify it as a NEW kilroy bug, halt the run via `kilroyHelp launch stopsafe`, fix the underlying cleanup logic in kilroy, redeploy, and resume.
- Default `--keep-parallel-passes` to 1 (most-recent only) — minimum disk pressure, sufficient for postmortem of the most recent fan-out attempt. Flag must be present in both `attractor run` and `attractor resume`.
- Do NOT delete the produced worktree directory on darkfactory unless explicitly asked. The whole point is to KEEP the final compiled artifact.
- Context: kilroyHelp dispatcher and darkfactorySetup.sh live OUTSIDE this repo at `/Users/travis/workspace/x85446/creds/` (unversioned per prior user decision). Edit them in place; don't try to commit them to this repo.
- Context: the dead run `run-20260617T211800Z` has already been deleted on darkfactory (disk recovered to ~6%). No salvage work is needed on it.
- Context: the journal banner `lacks --no-stage-archive-stacking` is emitted by `kilroyHelp.cmd_run` line 461 (and the resume equivalent at line 602). When adding the new flag, mirror the same probe+banner pattern so the operator can see at a glance whether the deployed binary supports each flag.
- Context: every kilroyHelp / darkfactorySetup change must be self-contained — the deploy loop relies on `darkfactorySetup.sh` being re-runnable as the single entry point.

## Oracle context applied

No project oracle.md found at `./.claude/data/oracle.md`. Global oracle at `~/.claude/skills/oracle/known.md` is empty (no buzzword index entries). Planned without oracle augmentation.

## Decisions log

2026-06-18T05:18:00Z — Design note for the fix:

- **Cleanup hook location**: at the START of `dispatchParallelBranches()` in `internal/attractor/engine/parallel_handlers.go` (~line 218), immediately after `passNum := exec.Engine.nextParallelPassCount(sourceNodeID)`. Prune passes with `pass_number ≤ passNum - keep` BEFORE spawning the new pass's worktrees. Rationale:
  - Prior pass results are already merged into `exec.WorktreeDir` (via fan-in / box-join in the previous tick of the engine loop), so the on-disk worktrees of older passes contain no in-flight or unmerged state.
  - Pre-pass pruning is idempotent on resume: whatever pass starts next handles cleanup; no need to persist cleanup state.
  - The engine loop is single-threaded between fan-out invocations, so there is no concurrency hazard inside `dispatchParallelBranches`.
- **Default retention**: `1` — keep only the most-recent fan-out pass on disk. Older passes are still recoverable from git via the `attractor/run/<runid>/parallel/<nodeID>/passN/<key>` branches that the engine creates per-pass. Decision: branches themselves are NOT deleted by this pass-cleanup — only the on-disk worktree dirs are removed. This preserves git history for postmortem/inspection while reclaiming the bulk of the disk (worktrees are the bloat, branches are cheap).
- **CLI flag name**: `--keep-parallel-passes <N>` on both `attractor run` and `attractor resume`. Threaded through `engine.RunOptions.KeepParallelPasses int` (0 means "use default of 1"; -1 means "disabled — old retain-everything behavior"; ≥1 means literal keep count). Default at the CLI layer is 1.
- **Helper function**: `func (e *Engine) pruneOldParallelPasses(logsRoot, parallelNodeID string, currentPass, keep int)` lives in `parallel_handlers.go`. Walks `logsRoot/parallel/<nodeID>/` for `pass<N>` dirs, for each `N` where `N + keep <= currentPass`: enumerates child `MM-<key>/worktree` subdirs, calls `GitOps.RemoveWorktree(repoPath, worktree)` to unregister from git, then `os.RemoveAll(passDir)` to free disk. Emits a `parallel_pass_pruned` progress event with `node_id`, `pruned_pass`, `bytes_reclaimed`, `keep_passes`.
- **No deletion of branch refs**: deferred — refs are cheap, the bloat is worktrees. If a future need to prune refs arises, the entry point is the same helper.

## Status / Log

2026-06-18T05:15:00Z — Step 1 design investigation complete; moving to implementation.
2026-06-18T05:18:00Z — Step 2: implementing helper + wiring CLI flag.
2026-06-18T06:45:00Z — Monitor tick: run active in expand_spec (5s lag), disk 6%, no failures. No intervention needed.
2026-06-18T06:46:30Z — Monitor tick: still in expand_spec (LLM streaming, 55s since last event — normal silence), unit active, disk 6%.
2026-06-18T06:47:30Z — Tick: still expand_spec, event count 26 (heartbeat advance), disk 6%, no failures.
2026-06-18T06:48:30Z — Tick: still expand_spec (~9min in), events 27, disk 6%, healthy.
2026-06-18T06:49:30Z — Tick: progress! expand_spec done, check_dod done (needs_dod), in dod_fanout (branch dod_c). 88 events, 1s lag, disk 6%.
2026-06-18T06:50:30Z — Tick: dod_fanout/dod_c, 181 events (+93/min), disk 6%, healthy.
2026-06-18T06:51:30Z — Tick: dod_fanout/dod_c, 274 events (+93/min steady), disk 6%.
2026-06-18T06:52:30Z — Tick: dod_fanout/dod_c, 367 events, disk 6%.
2026-06-18T06:53:30Z — Tick: dod_fanout shifted to branch dod_b, 439 events, disk 6%.
2026-06-18T06:54:30Z — Tick: dod_fanout DONE (success), now in consolidate_dod. 469 events, disk 6%.
2026-06-18T06:55:30Z — Tick: still in consolidate_dod (LLM streaming), 470 events, disk 6%.
2026-06-18T06:56:30Z — Tick: consolidate_dod ongoing, 471 events, disk 6%.
2026-06-18T06:57:30Z — Tick: consolidate_dod still in flight, 472 events, disk 6%.
2026-06-18T06:58:30Z — Tick: consolidate_dod in flight, 473 events, disk 6%.
2026-06-18T06:59:30Z — Tick: consolidate_dod ~10min in, 474 events, disk 6%.
2026-06-18T07:00:30Z — Tick: consolidate_dod ~11min in, 475 events, disk 6%.
2026-06-18T07:01:30Z — Tick: consolidate_dod DONE (success), now in plan_fanout (branch plan_b). 504 events, disk 6%.
2026-06-18T07:02:30Z — Tick: plan_fanout/plan_b, 609 events (+105/min), disk 6%.
2026-06-18T07:03:30Z — Tick: plan_fanout/plan_b, 702 events, disk 6%.
2026-06-18T07:04:30Z — Tick: plan_fanout/plan_b, 795 events, disk 6%.
2026-06-18T07:05:30Z — Tick: plan_fanout/plan_b, 888 events, disk 6%.
2026-06-18T07:06:30Z — Tick: plan_fanout/plan_b, 981 events, disk 6%.
2026-06-18T07:07:30Z — Tick: plan_fanout/plan_b, 1083 events, disk 6%.
2026-06-18T07:08:30Z — Tick: plan_fanout/plan_b, 1124 events, disk 6%.
2026-06-18T07:09:30Z — Tick: plan_fanout/plan_b, 1155 events, disk 6%.
2026-06-18T07:10:30Z — Tick: plan_fanout/plan_b, 1186 events, disk 6%.
2026-06-18T07:11:30Z — Tick: plan_fanout DONE (success), now in debate_consolidate. 1212 events, disk 6%.
2026-06-18T07:12:30Z — Tick: debate_consolidate ongoing, 1213 events, disk 6%.
2026-06-18T07:13:30Z — Tick: debate_consolidate ongoing, 1214 events, disk 6%.
2026-06-18T07:14:30Z — Tick: debate_consolidate ongoing, 1215 events, disk 6%.
2026-06-18T07:15:30Z — Tick: debate_consolidate ongoing, 1216 events, disk 6%.
2026-06-18T07:16:30Z — Tick: debate_consolidate ongoing, 1217 events, disk 6%.
2026-06-18T07:17:30Z — Tick: debate_consolidate ongoing, 1218 events, disk 6%.
2026-06-18T07:18:30Z — Tick: debate_consolidate ongoing, 1219 events, disk 6%.
2026-06-18T07:19:30Z — Tick: debate_consolidate ongoing (~14min in), 1220 events, disk 6%.
2026-06-18T07:20:30Z — Tick: debate_consolidate ongoing (~15min in), 1221 events, disk 6%.
2026-06-18T07:21:30Z — Tick: debate_consolidate ongoing, 1222 events, disk 6%.
2026-06-18T07:22:30Z — Tick: debate_consolidate DONE (success), now in analyze_fanout (branch analyze_launcher_crates). 1365 events, disk 6%.
2026-06-18T07:23:30Z — Tick: analyze_fanout/analyze_launcher_crates, 1485 events (+120/min), disk 6%.
2026-06-18T07:24:30Z — Tick: analyze_fanout/analyze_launcher_crates, 1613 events, disk 6%.
2026-06-18T07:25:30Z — Tick: analyze_fanout shifted to analyze_kernel_image_signing branch, 1768 events, disk 6%.
2026-06-18T07:26:30Z — Tick: analyze_fanout/analyze_kernel_image_signing, 1904 events, disk 6%.
2026-06-18T07:27:30Z — Tick: analyze_fanout/analyze_build_system, 1998 events, disk 6%.
2026-06-18T06:40:00Z — Steps 5/6/7 done.
  - Commit 4d3ded2 pushed to origin/main with the test file + final main.go wiring.
  - kilroyHelp: cmd_build_install now verifies BOTH --no-stage-archive-stacking and --keep-parallel-passes on run + resume.
  - kilroyHelp: new `_keep_parallel_passes_flag()` probe added (mirrors _stacking_flag pattern); cmd_run and cmd_resume now pass `--keep-parallel-passes 1` (default; KILROY_KEEP_PASSES env var overrides) and emit a "lacks" banner when binary is too old.
  - darkfactorySetup.sh deploy: kilroy 0.1.0+4d3ded2.main.dirty (the .dirty is from continuous active.md heartbeat — expected, the iterator is mid-execution).
  - Fresh run launched: run-20260618T063934Z with cmdline `kilroy attractor run ... --no-stage-archive-stacking --keep-parallel-passes 1`. Confirmed both flags present.
  - status: active, events 9s ago, in expand_spec; disk 6% used.
  - First launch attempt failed because `bash -lc` started at $HOME not at $PWD — wrote REPO=/home/travis instead of /home/travis/work/izcrOS. Retried with explicit `cd ~/work/izcrOS && kilroyHelp launch run` → success. Not a kilroy bug, not a kilroyHelp bug per se — `kilroyHelp launch run` correctly uses `$PWD`. Worth a future kilroyHelp safety check ("refuse to launch if PWD does not contain pipeline.dot"), filed mentally for later.
2026-06-18T05:35:00Z — Steps 2/3/4 done.
  - `engine.RunOptions.KeepParallelPasses int` added (engine.go); 0→default(1), -1→disabled, ≥1→literal.
  - `ResumeOverrides.KeepParallelPasses` added + threaded into opts (resume.go).
  - `pruneOldParallelPasses` helper + `dirSizeBytes` added (parallel_handlers.go).
  - Call site inserted right after `nextParallelPassCount` in `dispatchParallelBranches`.
  - `--keep-parallel-passes <n>` flag parser added to both `attractor run` and `attractor resume`; child-args propagation added for `--detach` path.
  - Usage strings updated for run + resume.
  - 5 new unit tests in `parallel_pass_cleanup_test.go` — ALL PASS.
  - Pre-existing parallel integration test failures (terminal_condition_edge on fixture .dot files) confirmed unchanged on `main`; NOT caused by my changes.
  - `go build ./cmd/kilroy` → clean.
  - `./kilroy attractor 2>&1 | grep keep-parallel` confirms flag visible in both usages.
