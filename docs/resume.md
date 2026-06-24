# Resume — handoff brief for next session

**Last updated:** 2026-05-18 (later)
**Audience:** the AI in the next Claude Code session (no prior context).

---

## TL;DR — what you're picking up

We have a working kilroy run (`run-20260507T074355Z`) that completed end-to-end on **darkfactory** and produced a Cargo workspace for IzOS Stage 0. We are now trying to **actually test what kilroy produced** by running `make test-qemu` (the real, non-mock variant) against the run's worktree.

That effort hit a wall in darkfactory's unprivileged LXC (device-mapper ioctls are blocked). Rather than weaken darkfactory's security, the plan pivoted to **standing up a new Incus VM (`kilroyfactor`) sized for kilroy work**, then run the test pipeline there. A bringup script has been written but not yet run.

**Two concrete next tasks, in order:**

1. Run `kilroy/scripts/darkfactory-bringup.sh` (defaults are fine) to launch the `kilroyfactor` VM on the `IncusOS:` remote.
2. Once it provisions cleanly end-to-end, take an Incus snapshot of the VM as a baseline:
   `incus snapshot create IncusOS:kilroyfactor baseline-clean`

**Recently completed (this session):**

- ✅ `darkfactory-bringup.sh` now deploys all `scripts/kilroy-*.sh` helpers (plus a copy of `darkfactory-bringup.sh` itself) into `/opt/darkfactory/scripts/` with mode 755. The new step runs inside the `KILROY_REPO` block right after the kilroy binary install — it reuses `$SRC=/root/projects/kilroy` (no second clone needed) and is idempotent (`install -m 755` overwrites). Verify with `ls /opt/darkfactory/scripts/` after bringup.

After that, the active goal (a.k.a. *"the kilroy-s5 piece"*) is to get the test-qemu pipeline working on `kilroyfactor` and extend `kilroy-s5-results.sh` (it currently only has `cdlatest`).

---

## Where things live

| Thing | Path |
|---|---|
| This repo | `/Users/travis/workspace/x85446/kilroy/` (Mac, branch `bug-89-fix-with-flag`) |
| Bringup script | `kilroy/scripts/darkfactory-bringup.sh` |
| Helper scripts | `kilroy/scripts/kilroy-*.sh` (10 files — see catalog below) |
| kilroy fork | `https://github.com/x85446/kilroy.git`, branch `bug-89-fix-with-flag` (HEAD `500e8d9 feat: add --no-stage-archive-stacking flag`) |
| Working run on darkfactory | `/home/travis/.local/state/kilroy/runs/run-20260507T074355Z/` |
| Worktree of completed run | same dir + `/worktree` |
| Final run archive | same dir + `/run.tgz` (88.6 GB) |
| `darkfactory` host | Incus container on `IncusOS:` at `10.0.169.24`, reachable as `ssh darkfactory` |
| Incus remote (admin) | `IncusOS:` → `https://localhost:2943` (forwarded to the IncusOS host) |

---

## Script catalog (all in `kilroy/scripts/`)

These all originated in `/opt/darkfactory/scripts/` on darkfactory; copies are now in this repo. `darkfactory-bringup.sh` now deploys them all to `/opt/darkfactory/scripts/` on the new VM as part of its provisioning phase.

| Script | Purpose |
|---|---|
| `darkfactory-bringup.sh` | Launch/reconcile an Incus VM provisioned for kilroy dev. Idempotent. Installs C/C++/Go/Rust + cargo-zigbuild + zig + cosign + ~50 apt packages + clones & builds kilroy from x85446 fork. Defaults: `--name kilroyfactor --type C8-M32 --disk 300GiB --remote IncusOS:`. |
| `kilroy-build-install.sh` | Build kilroy from a local source dir, verify the `--no-stage-archive-stacking` flag is present, install to `/usr/local/bin/kilroy`. |
| `kilroy-s1-ingest.sh` | Stage 1: ingest source into kilroy CXDB. |
| `kilroy-s2-pipeclean.sh` | Stage 2: pipe-clean — sanity validation of the ingested pipeline. |
| `kilroy-s3-run.sh` | Stage 3: run kilroy attractor (fresh). Passes `--no-stage-archive-stacking`. |
| `kilroy-s4-resume.sh` | Stage 4: resume from last run / checkpoint. Has a `heal_checkpoint` workaround for an upstream kilroy bug where `checkpoint.json` is saved before the git commit event lands. Passes `--no-stage-archive-stacking`. |
| `kilroy-s5-results.sh` | Stage 5: inspect results. Currently has only `cdlatest` (prints worktree path of newest successful run, for `cd "$(kilroy-s5-results.sh cdlatest)"`). **Needs more subcommands** — see open items. |
| `kilroy-launch-detached.sh` | Front-end with `resume`/`run`/`status`/`stop`/`logs` subcommands. Writes `/etc/kilroy-run.env` and starts `kilroy-run.service` (systemd, system-slice so it survives ssh disconnect). |
| `kilroy-system-runner.sh` | Internal: invoked by `kilroy-run.service`; reads `KILROY_MODE/KILROY_ARGS` from `/etc/kilroy-run.env`, execs s3 or s4 wrapper. |
| `kilroy-activeCleanup.sh` | Safely free disk while a kilroy run is in flight (predates this session). |
| `kilroy-catchpipe.sh` | Snapshot a `pipeline.dot` (or any file) on each size change (predates this session). |

---

## The kilroyfactor plan (current focus)

We're moving the testing surface OFF darkfactory (unprivileged LXC, hits dm/loop walls) and onto a **dedicated VM** sized for the workload. The script implements this:

```bash
cd /Users/travis/workspace/x85446/kilroy/scripts
./darkfactory-bringup.sh                        # defaults: kilroyfactor C8-M32 300GiB
# or:
./darkfactory-bringup.sh --name kilroyfactor --type C16-M64 --disk 500GiB
```

After it returns successfully:
```bash
incus snapshot create IncusOS:kilroyfactor baseline-clean
```

The helper-deployment step is now built in (added 2026-05-18): right after the kilroy binary install, the remote script iterates `$SRC/scripts/kilroy-*.sh` (plus `darkfactory-bringup.sh` itself) and `install -m 755`s each into `/opt/darkfactory/scripts/`.

Still a gap: `kilroy-s4-resume.sh` and `kilroy-launch-detached.sh` expect things like `/etc/kilroy-run.env`, `kilroy-run.service` (a systemd unit), and a `cliproxy-status.sh` helper. The bringup script does NOT install any of that infrastructure — close this gap if you want the systemd-based launch flow to work on `kilroyfactor`.

---

## Why we needed a new machine — the test-qemu story so far

I tried to run `make test-qemu` on `darkfactory` against the kilroy run worktree. Kilroy emitted a half-working pipeline; below are the 10 patches I had to apply just to push the workflow further down the pipeline. **Treat each one as a likely kilroy upstream bug worth filing** (they all live in the worktree at `/home/travis/.local/state/kilroy/runs/run-20260507T074355Z/worktree`):

1. **Dockerfile.build**: `cosign` isn't in Debian bookworm apt. Replaced with `RUN curl ... cosign-linux-amd64` from GitHub release (v2.4.1).
2. **Dockerfile.build**: `cargo install --locked cargo-zigbuild` picks latest (0.22.3) which requires rustc ≥1.88. Pinned to `--version 0.20.1` (compatible with rust 1.83).
3. **Dockerfile.build**: `cargo-nextest` line had `--locked` twice (invalid).
4. **Dockerfile.build**: `cargo-nextest 0.9.97` requires rustc ≥1.85, `cargo-llvm-cov` latest probably the same. Removed both — they're only used by `validate-test.sh` which gracefully falls back to `cargo test`.
5. **Dockerfile.build**: missing `zig` runtime (cargo-zigbuild depends on it). Added install of ziglang 0.13.0 from official tarball.
6. **Worktree `.cargo/config.toml`**: didn't exist. `cargo build --target aarch64-unknown-linux-gnu` was invoking host `cc` for linking. Added `[target.aarch64-unknown-linux-gnu] linker = "aarch64-linux-gnu-gcc"` + matching CC/CXX/AR env vars.
7. **Incus container config** (darkfactory): no `/dev/loop*` or `/dev/loop-control` visible inside the unprivileged container. Added live:
   ```
   incus config device add darkfactory loop-control unix-char major=10 minor=237 path=/dev/loop-control
   for i in 0 1 2 3 4 5; do
     incus config device add darkfactory loop$i unix-block source=/dev/loop$i path=/dev/loop$i
   done
   ```
   (loop6/loop7 don't exist on the host so they error — fine.)
8. **Dockerfile.test**: added `kpartx` to apt install.
9. **scripts/build-disk-image.sh**: `losetup --partscan` doesn't materialize `/dev/loopXpN` device nodes inside docker (no udev). Replaced with `losetup -f --show` + `kpartx -av` and substituted `${LOOP}pN` with `/dev/mapper/loopXpN`. Cleanup also `kpartx -dv` before `losetup -d`.
10. **makehelp.sh `sign_all_artifacts`**: kilroy's `enroll-tpm-keys` (which generates the CI signing key) runs LATER in `run-qemu` — but `target_sign` runs first during `make all`, so every `sign-artifact` call dies with "signing key required: set COSIGN_KEY" and the script swallows it silently then logs "all artifacts signed". Patched to generate a cosign keypair at the top of `sign_all_artifacts` and `export COSIGN_KEY=crates/izos-crypto/keys/ci-signing-key.key` before iterating.

**Where it died finally:** patch 9's `kpartx` triggered:
```
/dev/mapper/control: mknod failed: Operation not permitted
device-mapper: version ioctl on  failed: Permission denied
device mapper prerequisites not met
```
Even after exposing `/dev/mapper/control` via Incus, the unprivileged-LXC user namespace blocks dm ioctls. To proceed, the container needs `security.privileged=true`. The user agreed to the toggle but the **harness hook blocked the `incus stop && incus config set ... privileged true && incus start` sequence** (correctly so — the hook viewed it as a major security weakening of shared infra that wasn't explicit enough in chat). The user then redirected to: skip darkfactory, build a clean new VM (kilroyfactor) where the security model already permits dm + loop natively. That's where you're picking up.

If the user revisits the darkfactory privileged toggle later, the command to run with the `!` prefix is:
```
! incus stop IncusOS:darkfactory && incus config set IncusOS:darkfactory security.privileged true && incus start IncusOS:darkfactory && sleep 30 && incus list IncusOS:darkfactory
```
This is a metadata-only ZFS UID shift (~1–5 min). Container root then equals host root — significant security shift; only do it if asked again explicitly.

---

## State of darkfactory right now

- Container is still **unprivileged** (the privileged toggle was never executed).
- Per-session live additions that remain in the Incus config: 6 loop devices (`loop0..loop5`), `loop-control`, `dm-control` unix-char.
- The last `make test-qemu` background process is dead (PID was 1311325 in `/tmp/test-qemu-current-pid`). Logs sit in `/tmp/test-qemu-*.log`.
- The `kilroy-run.service` from the successful 06:38–11:03 run has long since deactivated cleanly.
- The kilroy worktree on darkfactory still has all the patches above applied. If you ever revive testing on darkfactory, you can:
  ```bash
  ssh darkfactory 'cd $(/opt/darkfactory/scripts/kilroy-s5-results.sh cdlatest) && git diff'
  ```
  to see the local changes.

---

## Open items / nice-to-haves (after task #1-3)

- **Extend `kilroy-s5-results.sh`** with more subcommands (user asked about additions). Candidates: `list` (all runs with status), `status` (compact summary of latest), `logs` (tail of latest progress.ndjson), `artifacts` (show built outputs under `out/<arch>/`). Pattern is already structured — add `cmd_X()` + a case in the dispatch.
- **File upstream bugs against kilroy** for the 10 patches above (each is an independent issue in kilroy's generated Dockerfile / makehelp / scripts).
- **Decide whether to refactor `build-disk-image.sh` to skip device-mapper entirely** — use `losetup -f -o <offset> --sizelimit <size>` per partition. Doesn't need kpartx / dm-control / privileged anything; works in any container.
- **`darkfactory-bringup.sh` does not currently provision the systemd kilroy-run.service or `/etc/kilroy-run.env`** — needed for `kilroy-launch-detached.sh` to work. Add to bringup if/when you want the detached-run flow on `kilroyfactor`.

---

## Useful one-liners for verification

```bash
# what darkfactory looks like to Incus right now
incus list IncusOS:darkfactory
incus config show IncusOS:darkfactory | grep -A1 security

# what the completed kilroy run produced
ssh darkfactory '/opt/darkfactory/scripts/kilroy-s5-results.sh cdlatest'

# bringup the new VM (NOT YET RUN)
cd /Users/travis/workspace/x85446/kilroy/scripts
./darkfactory-bringup.sh --help
./darkfactory-bringup.sh                      # launch kilroyfactor

# after kilroyfactor is up
incus exec IncusOS:kilroyfactor -- bash
incus snapshot create IncusOS:kilroyfactor baseline-clean
incus snapshot list IncusOS:kilroyfactor
```

---

## Usage gate (`kilroyHelp gate`)

`kilroyHelp launch run/resume` start a usage gate that keeps a run inside an
account-usage budget. It reads Anthropic's real 5h/7d windows from
`GET https://api.anthropic.com/api/oauth/usage` (auth: the OAuth token
cli-proxy-api already stores in `~/.cli-proxy-api/claude-*.json` + header
`anthropic-beta: oauth-2025-04-20`) — headless, no Mac session needed. At each
safe stage boundary (top-level `node.completed`) it evaluates the budget and
either lets the run continue or `launch stopsafe`s it; while parked it
re-checks every `POLL_INTERVAL_S` and `launch resume`s once the windows clear.
In-flight stages always finish — the gate never kills mid-stage.

Subcommands: `kilroyHelp usage` (print 5h/7d utilization), `gate --check`
(one verdict), `gate --selftest` (assert threshold tables), `gate --show-config`,
`gate --status`, `gate run` (the loop; started automatically by launch).

All thresholds live in `/etc/kilroy-usage-gate.conf`, re-read on every
evaluation — edit it on disk and the next stage obeys, no restart:

- `MODE` = `logical` (envelope + weekly pace) | `stopnext` (park at next
  boundary) | `burnout` (ignore all limits).
- 5h envelope: `T(h) = clamp(T5H_ANCHOR_H1 + (H5−H1)/4·(h−1), H1, H5)`,
  default 50%@1h → 80%@5h.
- Weekly guard: park if `util_7d > WEEKLY_PACE_MULT·(days/7)·100` (default 2×,
  the statusline "red" line), capped at `WEEKLY_CEILING` (90%).
- `FRESH_RUN_CAP` (85%): refuse to *start* a fresh `launch run` at/above this
  (resume is exempt; `MODE=burnout` overrides).
- Burnout: `BURNOUT_ARMED=1` bypasses all limits when the weekly reset is within
  `BURNOUT_WINDOW_H` hours (set 10 for the last two 5h blocks); never self-arms.

The gate logs every evaluation (and per-stage utilization burn) to
`/var/log/kilroy-usage-gate.log`, falling back to `~/kilroy-usage-gate.log`.

Caveat: using a subscription OAuth token through cli-proxy-api violates
Anthropic's consumer ToS and is detectable (datacenter IP, missing Claude Code
telemetry, sustained volume) — account-suspension risk, accepted for this box.

---

## Conversation context the next AI should know

- User: travis (travis.mccollum@gmail.com). Working from Mac at `cypressMini` (the directory `~/workspace/x85446/kilroy/` on Mac is the kilroy fork; `~/workspace/x85446/creds/` is a separate working dir where the live Claude Code session is rooted).
- User prefers terse responses, single bundled PRs for refactors, no rebuild-from-scratch when resume works.
- User has called `/monitor` repeatedly to babysit long-running builds — that pattern works well here.
- The `cliproxyapi.service` running on darkfactory is critical for kilroy's Claude OAuth-to-API access. If you ever stop darkfactory, it auto-restarts on boot but in-flight kilroy work would die.
- darkfactory's storage backend is ZFS — privileged transitions are metadata-only (fast).
