# Iterate Task — config-driven usage gate + auto-resume for kilroyHelp launch

Started: 2026-06-23T16:00:00Z (planned), executing from 2026-06-23T21:06:40Z
CWD: /Users/travis/workspace/x85446/kilroy
phase: executing
running: 2026-06-23T21:06:40Z
loop_job: f68157bc

## Goal
Add a config-file-driven, usage-aware gate to `kilroyHelp launch run/resume/stopsafe` that, at each safe stage boundary, reads a darkfactory-local config file plus Anthropic's `/api/oauth/usage` and parks or resumes the kilroy run to stay within a time-scaled 5-hour burn envelope and a weekly daily-pace guard — with operator modes (logical | stopnext | burnout), opt-in burnout near the weekly reset, and automatic resume once the windows clear.

## Steps

1. Add `_probe_usage` to kilroyHelp (`/Users/travis/workspace/x85446/creds/kilroyHelp`): read the OAuth `access_token` from cli-proxy-api's auth file (`~/.cli-proxy-api/claude-*.json`) into a variable that is NEVER printed, `GET https://api.anthropic.com/api/oauth/usage` with `Authorization: Bearer <token>` + `anthropic-beta: oauth-2025-04-20`, and parse the body into `USAGE_5H_PCT`, `USAGE_5H_RESET`, `USAGE_7D_PCT`, `USAGE_7D_RESET`. Re-read the token file on every call so cli-proxy-api's background refresh is picked up; treat HTTP 401 as "token stale, retry next tick" (no crash). Add a `kilroyHelp usage` subcommand that prints the four values + HTTP status.

2. Add the gate config file + loader. Default path `/etc/kilroy-usage-gate.conf`, env-style `KEY=value`, auto-created with documented defaults when absent. Keys: `MODE` (logical|stopnext|burnout), `T5H_ANCHOR_H1=50`, `T5H_ANCHOR_H5=80`, `FRESH_RUN_CAP=85`, `WEEKLY_PACE_MULT=2.0`, `WEEKLY_CEILING=90`, `BURNOUT_WINDOW_H=5`, `BURNOUT_ARMED=0`, `POLL_INTERVAL_S=120`. `_load_gate_config` re-sources the file on EVERY call so an on-disk edit takes effect at the next read — no restart. Add `kilroyHelp gate --show-config`.

3. Implement the threshold math as pure functions. `_threshold_5h(h)` = clamp(`T5H_ANCHOR_H1` + (`T5H_ANCHOR_H5`−`T5H_ANCHOR_H1`)/4·(h−1), `T5H_ANCHOR_H1`, `T5H_ANCHOR_H5`). `_threshold_7d(d)` = min(`WEEKLY_CEILING`, `WEEKLY_PACE_MULT`·(d/7)·100). Elapsed time derived from the API reset times: `window_start = reset − span`, `h`/`d` = now − window_start, clamped to the span. Add `kilroyHelp gate --selftest` that prints and asserts both threshold tables.

4. Implement `_gate_decide` → `ALLOW` / `PARK <reason>` from current usage + loaded config + mode. Logic: `MODE=stopnext` → PARK; burnout active (`MODE=burnout`, OR `BURNOUT_ARMED=1` and time-to-weekly-reset ≤ `BURNOUT_WINDOW_H`h) → ALLOW unconditionally; else `MODE=logical` → PARK if `USAGE_5H_PCT > _threshold_5h(h)` OR `USAGE_7D_PCT > _threshold_7d(d)` OR `USAGE_7D_PCT > WEEKLY_CEILING`, else ALLOW. Honor test-injection env (`GATE_TEST_5H`, `GATE_TEST_7D`, `GATE_TEST_7D_RESET`) so verdicts are unit-checkable. Add `kilroyHelp gate --check`.

5. Implement the gate watcher loop (`kilroyHelp gate run`): tail the active run's `progress.ndjson` for top-level `node.completed` (the safe stop points); on each, re-load config, probe usage, call `_gate_decide`; on PARK → `kilroyHelp launch stopsafe` and record parked-state; log every evaluation (ts, node, h, d, util_5h, util_7d, T5h, T7d, mode, verdict, per-stage util-delta) to `/var/log/kilroy-usage-gate.log` (fallback `~/kilroy-usage-gate.log`). While parked, keep polling every `POLL_INTERVAL_S`; when `_gate_decide` → ALLOW, `kilroyHelp launch resume`. Never act mid-stage.

6. Wire the gate into `launch`: `launch run`/`resume` also start the gate loop alongside the run (systemd `kilroy-usage-gate.service` if available, else `nohup`), and refuse a *fresh* `launch run` when `USAGE_5H_PCT ≥ FRESH_RUN_CAP` (unless burnout) with a clear message. `stopsafe` stays the actuator. Register `gate` + `usage` in `KILROYHELP_ACTIONS` and dispatch. Preserve the existing 5-second post-launch auto-status.

7. Burnout auto-arm + safety. In `logical` mode, when `BURNOUT_ARMED=1` and the weekly reset is within `BURNOUT_WINDOW_H` hours, the gate logs "burnout armed — bypassing limits" and ALLOWs everything; `BURNOUT_WINDOW_H=10` extends coverage to the last two 5h blocks. With `BURNOUT_ARMED=0` (default) full limits hold right up to reset. Burnout is opt-in only — never self-enables without the armed flag or explicit `MODE=burnout`.

8. Deploy + document. Save kilroyHelp to creds, `scp` to `/opt/darkfactory/scripts/kilroyHelp`, `bash -n` syntax check, ensure `/etc/kilroy-usage-gate.conf` exists with defaults on darkfactory, and document the config file, the three modes, and the burnout opt-in in kilroyHelp help text and `docs/resume.md`. Have the gate log per-stage util-burn so the 50@1h / 80@5h anchors can be calibrated from real data later.

## Validation

1. `ssh darkfactory kilroyHelp usage` returns HTTP 200 and prints a `five_hour` utilization within ±3 points of the Mac statusline's current `rate_limits.five_hour.used_percentage` (cross-check `/tmp/sessiondata`), plus parseable `resets_at` for both the 5h and 7d windows. A deliberately stale token yields a logged "401 retry next tick", not a crash.

2. Delete `/etc/kilroy-usage-gate.conf` on darkfactory and run `kilroyHelp gate --show-config` → the file is recreated with all documented keys and `MODE=logical`. Then edit `MODE=stopnext` on disk and re-run `kilroyHelp gate --show-config` → output shows `stopnext` with no process restart.

3. `ssh darkfactory kilroyHelp gate --selftest` exits 0, printing the 5h table (h=0.5,1,2,3,4,5 → 50,50,57.5,65,72.5,80) and the weekly table (d=1,2,4 → 28.57,57.14,90-capped), each asserted equal to expected.

4. `kilroyHelp gate --check` with injected values returns the right verdict for each: (i) `GATE_TEST_5H=60` at h≈2 → PARK (5h>57.5); (ii) `GATE_TEST_5H=40 GATE_TEST_7D=20` at h≈2 → ALLOW; (iii) `GATE_TEST_7D=85` on day 1 → PARK (weekly pace); (iv) `MODE=stopnext` → PARK regardless; (v) `MODE=burnout GATE_TEST_5H=99` → ALLOW; (vi) `BURNOUT_ARMED=1` + `GATE_TEST_7D_RESET` 3h out + `GATE_TEST_5H=99` → ALLOW. Wrapper exits 0 on all-match.

5. Start `kilroyHelp gate run` against the resumed live run on darkfactory; `tail /var/log/kilroy-usage-gate.log` shows ≥1 `node.completed` evaluation line carrying real `util_5h`/`util_7d`, computed `T5h`/`T7d`, mode, and a verdict. Inject `GATE_TEST_5H=99` → within one `POLL_INTERVAL_S` the gate calls `stopsafe` and `kilroyHelp status` shows the unit parked; clear the injection → within one poll it `resume`s and `status` shows active again.

6. `ssh darkfactory "cd ~/work/izcrOS && kilroyHelp launch resume"` starts BOTH the run unit and the gate; `kilroyHelp launch status` (and `kilroyHelp gate --status`) shows the gate running with the current mode + live thresholds; the 5-second auto-status fires. With `GATE_TEST_5H=99` a fresh `kilroyHelp launch run` is refused citing "5h 99% ≥ cap 85", and is allowed when `MODE=burnout`.

7. `GATE_TEST_7D_RESET` 3h out + `BURNOUT_ARMED=1` + `GATE_TEST_5H=99` → `kilroyHelp gate --check` returns ALLOW and the log records "burnout armed"; flip `BURNOUT_ARMED=0` on disk → next `gate --check` returns PARK. `BURNOUT_WINDOW_H=10` + reset 8h out → ALLOW (two-block coverage); `BURNOUT_WINDOW_H=5` + reset 8h out → PARK.

8. `bash -n /opt/darkfactory/scripts/kilroyHelp` exits 0; `/etc/kilroy-usage-gate.conf` exists with every documented key; editing `MODE` on disk changes the next `gate --check` verdict with no restart; `docs/resume.md` describes the config file, the three modes, and the burnout opt-in; the gate log contains per-stage util-delta entries after ≥2 stage completions.

## Constraints
- kilroyHelp is UNVERSIONED at `/Users/travis/workspace/x85446/creds/kilroyHelp` — edit in place and deploy via `scp` to `/opt/darkfactory/scripts/kilroyHelp`. Never commit it to the kilroy repo.
- The config file is the single source of truth, re-read at every stage boundary; on-disk edits at darkfactory take effect at the next stage with NO restart. Default path `/etc/kilroy-usage-gate.conf`. `MODE` pointer values: `logical` (the burn-envelope plan) | `stopnext` (park at next safe boundary) | `burnout` (ignore all limits).
- The usage probe uses the OAuth token from cli-proxy-api's auth file (read-only usage GET). The token is NEVER printed/echoed; re-read it each tick so proxy-managed refresh is honored.
- The gate acts ONLY at top-level `node.completed` boundaries; an in-flight stage always finishes (kilroy has stages that cannot be stopped mid-flight). 85% is a fresh-run admission cap, not a mid-stage kill.
- Burnout is opt-in only (`BURNOUT_ARMED=1` or `MODE=burnout`) — it never self-enables. Auto-arm near the weekly reset requires `BURNOUT_ARMED=1`.
- Do NOT restart cli-proxy-api — it is actively serving and the run is parked/resumable.
- Default anchors ship at 50%@1h, 80%@5h, 85% fresh-run cap, 2× weekly daily-pace, 90% weekly ceiling, 120s poll, 5h burnout window. The gate logs per-stage util-burn so these can be recalibrated from real data.
- User-accepted: the cli-proxy-api + subscription-OAuth setup carries account-suspension risk under Anthropic policy; accepted for this account. This planning scope treats that risk as accepted (do not re-prompt).
- Reference: `/api/oauth/usage` returns `five_hour.{utilization,resets_at}`, `seven_day.{utilization,resets_at}`, `seven_day_sonnet`, and a `limits[]` array (kind/group/percent/severity/resets_at/is_active) — utilization is 0–100.

## Decisions log
(empty until execution)

## Status / Log
(empty until execution)
