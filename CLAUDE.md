# CLAUDE.md — operational guidance for this repo

This file is loaded into every Claude Code session in the kilroy repo. It
captures **operational** knowledge (how we run kilroy, where artifacts live,
recurring failure modes in kilroy's *output*). Two companion docs cover the
other layers:

- **`AGENTS.md`** — primary contributor guide. **Read it.** The Prime Directive
  lives there: when you're here, you're improving kilroy, not fixing the
  downstream project. If you see kilroy generating broken Dockerfiles or
  Makefiles for some target project, fix kilroy's generator — never paper
  over it in the target.
- **`docs/resume.md`** — session handoff. Holds the in-flight state of
  whatever the user was last working on. Check it at the start of a session
  if the user references prior context.

---

## kilroy vs spec — triage rule

kilroy is a darkfactory: feed it a spec, it builds what the spec defines.
When something about a run looks wrong, decide *which* thing is broken before
filing or fixing anything:

- **kilroy didn't exit cleanly** (crash, error, stuck, partial output) →
  **kilroy bug**. Fix kilroy itself (see AGENTS.md Prime Directive).
- **kilroy exited cleanly but the produced output doesn't have what you
  needed** → **spec bug**. The requirements you fed in didn't ask for
  that thing — or asked for it ambiguously. Fix the spec, then re-run.

"Good build, wrong result" is the giveaway for a spec bug; "good intent,
broken tool" is the giveaway for a kilroy bug. Both get filed, just in
different places.

---

## Operational hosts (where kilroy work happens)

| Name | Role | How to reach |
|---|---|---|
| `darkfactory` | Original Incus container; ran the first successful end-to-end kilroy run (`run-20260507T074355Z`). Unprivileged LXC — has dm/loop limitations for disk-image work. | `ssh darkfactory` (resolves to `10.0.169.24`); admin via `IncusOS:darkfactory`. |
| `kilroyfactor` | Newer dedicated Incus **VM** for kilroy dev/test. Avoids the privileged-LXC walls darkfactory hits. Provision with `scripts/darkfactory-bringup.sh`. | `incus exec IncusOS:kilroyfactor -- bash` |
| `IncusOS:` (Incus remote) | The Incus admin endpoint that hosts both. Reached locally on `https://localhost:2943` (SSH-forwarded to the host at 10.0.175.176:8443). | `incus list IncusOS:` |

**Do not confuse them:** the `darkfactory` directory at `/opt/darkfactory/`
exists *inside the darkfactory container* and holds operational scripts. The
name "darkfactory" is overloaded — host **and** scripts dir.

---

## Run lifecycle — the kilroy-s1 .. s5 convention

Operational helpers live in `scripts/kilroy-*.sh` and follow a numbered
pipeline. The same files are mirrored to `/opt/darkfactory/scripts/` on hosts
that run kilroy. They are documented here so future sessions don't reinvent
the convention:

| Stage | Script | What it does |
|---|---|---|
| s1 | `kilroy-s1-ingest.sh` | Ingest source into kilroy CXDB. |
| s2 | `kilroy-s2-pipeclean.sh` | Pipe-clean — sanity-validate the ingested pipeline. |
| s3 | `kilroy-s3-run.sh` | Start a fresh attractor run. Always passes `--no-stage-archive-stacking`. |
| s4 | `kilroy-s4-resume.sh` | Resume from last run / checkpoint. Workaround for kilroy bug where `checkpoint.json` is serialized before the `git commit` event lands (`heal_checkpoint` re-injects the sha from HEAD). |
| s5 | `kilroy-s5-results.sh` | Inspect run results. Currently exposes `cdlatest` → prints worktree path of the most recent successful run. Designed to grow more subcommands (`list`, `status`, `logs`, `artifacts`). |

Plus three supporting scripts:

- `kilroy-build-install.sh` — build kilroy from a local source dir, verify the
  `--no-stage-archive-stacking` flag is present, install to `/usr/local/bin/kilroy`.
- `kilroy-launch-detached.sh` — front-end with `resume`/`run`/`status`/`stop`/
  `logs` subcommands. Writes `/etc/kilroy-run.env` and starts the
  `kilroy-run.service` systemd unit (system-slice, survives ssh disconnect).
- `kilroy-system-runner.sh` — internal: invoked by `kilroy-run.service`; reads
  env file and execs s3 or s4.

**Usage idiom for cd'ing into the latest successful run worktree:**
```bash
cd "$(/opt/darkfactory/scripts/kilroy-s5-results.sh cdlatest)"
```

---

## ALWAYS run through the usage gate (never bypass it)

**Rule: start and resume runs with `kilroyHelp launch` — NEVER with
`systemctl start kilroy-run.service` directly.**

The token throttle is the **usage-gate daemon**. It polls Anthropic's OAuth
usage endpoint (5-hour primary + 7-day secondary windows) and *parks* the run
at the next node boundary when a burn envelope is exceeded, then auto-resumes
when the window resets. `kilroyHelp launch resume|run` starts that daemon
alongside the run (`_gate_start_daemon`). The systemd unit's `ExecStart`
(`kilroyHelp _system-runner`) does **not** — so `systemctl start
kilroy-run.service` runs the engine with **zero throttling** and will burn
straight through your subscription quota (observed: 894 requests / 112 rate-limit
hits / 23% of the 7-day window in ~2h, tokens killed).

- Resume the current run:  `cd <repo> && kilroyHelp launch resume`
- Fresh run:               `cd <repo> && kilroyHelp launch run`
- Stop:                    `kilroyHelp launch stop` (or `stopsafe` to finish the
  in-flight node first)

Do **not** reach for the `systemctl start` path as a "avoid graph churn"
shortcut. Churn (the dual-AI strategy pipeclean) is controlled by the gate
config's `STRATEGY`, not by whether the gate runs: set `STRATEGY=anthropic-tiers`
in `/etc/kilroy-usage-gate.conf` to keep an all-anthropic run all-anthropic
(no pipeclean, no OpenAI wiring) while still getting the parking throttle.
Gate config lives at `/etc/kilroy-usage-gate.conf`; `MODE=logical` applies the
5h burn envelope + weekly pace guard. The gate throttles **everything** the run
sends through cliproxyapi, including the escalation ladder's lever-#3 diagnosis
agents.

---

## Run-artifact layout

A completed run on either host lives at:

```
$HOME/.local/state/kilroy/runs/run-YYYYMMDDTHHMMSSZ/
├── worktree/             # git worktree on branch attractor/run/run-<id>
│   ├── Cargo.toml, crates/, bins/, Makefile, scripts/, Dockerfile.build, Dockerfile.test, ...
│   └── .ai/runs/<id>/test-evidence/latest/   # captured serial logs, sig-verify, slot-state, PCR dumps
├── progress.ndjson       # all events (node/stage transitions, heartbeats, run_completed)
├── checkpoint.json       # resume state
├── run.tgz               # final bundled archive (only after run_completed)
└── parallel/             # per-branch worktrees from fan-out stages
```

The worktree's `.git` is a **file** (62 bytes pointing to a kilroy-managed
commondir), not a directory. `git -C <worktree>` works normally; many shell
checks for `.git` use `-d` and silently fail — prefer `-e`.

---

## Recurring failure modes in kilroy's generated artifacts

These are bugs in what kilroy *produces*. If you encounter them in a fresh
run, **fix them in kilroy** (per the Prime Directive in AGENTS.md), not in
the target. They have all been seen at least once:

1. **`Dockerfile.build` apt-installs `cosign`** — not packaged in Debian
   bookworm. Need to install from `https://github.com/sigstore/cosign/releases`.
2. **`Dockerfile.build` does `cargo install --locked cargo-zigbuild`
   unpinned** — current cargo-zigbuild requires rustc ≥1.88, kilroy pins
   rust 1.83. Need a `--version` pin (last known good: 0.20.1).
3. **`Dockerfile.build` has `cargo install --locked cargo-nextest --locked`**
   (literally `--locked` twice). Invalid since recent cargo.
4. **`Dockerfile.build` `cargo install`s cargo-nextest / cargo-llvm-cov
   unpinned** — same MSRV problem as #2. They are also unnecessary for
   `make test-qemu`; `validate-test.sh` falls back to `cargo test` if missing.
5. **`Dockerfile.build` installs `cargo-zigbuild` but not `zig`** —
   cargo-zigbuild needs `zig` at runtime. Install from ziglang.org tarball.
6. **No `.cargo/config.toml`** — cross-compile of `aarch64-unknown-linux-gnu`
   binaries fails at link step because cargo defaults to host `cc`. Need a
   `[target.aarch64-unknown-linux-gnu] linker = "aarch64-linux-gnu-gcc"` plus
   CC/CXX/AR env vars.
7. **`scripts/build-disk-image.sh` uses `losetup --partscan`** — partition
   device nodes (`/dev/loopXpN`) don't materialize in containers without
   udev. Use `losetup -f --show` + `kpartx -av` and read partitions from
   `/dev/mapper/loopXpN`. Cleanup also needs `kpartx -dv` before `losetup -d`.
8. **`Dockerfile.test` does not install `kpartx`** — needed by the fix above.
9. **`makehelp.sh sign_all_artifacts` runs before `enroll-tpm-keys`
   generates the CI signing key** — so `cosign` has no `COSIGN_KEY`, every
   `sign-artifact.sh` invocation errors, the loop ignores errors and falsely
   logs "all artifacts signed". Generate an ephemeral cosign keypair at the
   top of `sign_all_artifacts` and `export COSIGN_KEY` before iterating.

These are independent bugs and each is worth a separate upstream issue.

---

## Environment gotchas (the docker-in-LXC stack)

- **Unprivileged LXC has no loop devices by default.** Add via
  `incus config device add <ct> loopN unix-block source=/dev/loopN
  path=/dev/loopN` for whatever N exist on the host.
- **Unprivileged LXC blocks device-mapper ioctls** even when
  `/dev/mapper/control` is exposed. dm tools (kpartx, lvm, dmsetup) won't
  work without `security.privileged=true`. Toggling privileged on a live
  shared container is a real security shift and **must have explicit user
  authorization in chat** before executing — the harness hook will (and
  should) block it otherwise. Sidestepping options:
  - run image builds in a **VM** instead (`kilroyfactor` was created for
    exactly this reason; see `scripts/darkfactory-bringup.sh`).
  - rewrite `build-disk-image.sh` to use `losetup -f -o <offset>
    --sizelimit <size>` per partition (avoids dm entirely; not yet done).
- **Nested KVM** for `--enable-kvm` qemu requires it on the LXC host and
  /dev/kvm passthrough. Untested in our stack; expect TCG fallback.

---

## Provisioning a new dev host

```bash
cd scripts
./darkfactory-bringup.sh                                    # defaults: kilroyfactor C8-M32 300GiB
./darkfactory-bringup.sh --name df-austin --type C16-M64 --disk 500GiB
./darkfactory-bringup.sh --remote H91:                      # different incus remote
```

The script is idempotent — re-run any time to install newly-added packages
listed in the `APT_PACKAGES=( ... )` array at the top of the file (or new
toolchain versions). After a clean first provisioning, take a baseline
snapshot:

```bash
incus snapshot create IncusOS:<vm-name> baseline-clean
```

`darkfactory-bringup.sh` now installs both the `kilroy` binary AND deploys
all `scripts/kilroy-*.sh` helpers (plus a copy of itself) into
`/opt/darkfactory/scripts/` on the new VM. Still missing: the systemd
`kilroy-run.service` unit and `/etc/kilroy-run.env` that
`kilroy-launch-detached.sh` expects — see `docs/resume.md` open items.

---

## When in doubt

- Improving kilroy itself → read **AGENTS.md** (Prime Directive lives there).
- What was the user just working on → read **`docs/resume.md`**.
- How to operate a run, where artifacts live, recurring downstream failures →
  this file.
- "How do I kick off a kilroy session on darkfactory against a repo?" →
  **`docs/kilroy-getting-started.md`** (operator quickstart, worked example
  against `~/workspace/izuma/izcrOS`).
