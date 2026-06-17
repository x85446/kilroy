# About: kilroy and the darkfactory concept

For an AI new to this codebase. Read alongside `AGENTS.md` (contributor
Prime Directive), `CLAUDE.md` (operational), `docs/kilroy-getting-started.md`
(operator quickstart), `docs/runs-layout.md` (artifact anatomy).

---

## What a darkfactory is

A **darkfactory** is a lights-out software factory: feed it a written
specification and it produces a buildable, testable software repository
end-to-end with no human in the loop during the build. The name borrows
from manufacturing — a "dark factory" runs without people on the floor.

Contract: spec in → product repo out. If the produced repo is broken, the
bug is either in the spec or in the factory. Never patched downstream.

---

## What kilroy is

**kilroy** is one implementation of the darkfactory pattern. It operates on
a target git repo whose **`docs/*.md` is the specification** of a system to
be built. kilroy reads those docs, plans a multi-stage LLM pipeline to
deliver the system, executes the pipeline, and commits the produced sources
(Cargo crates, Dockerfiles, Makefiles, scripts, disk-image build, signing,
test harness, …) onto a worktree branch `attractor/run/run-<id>` of that
same repo.

The host VM where kilroy runs is named `darkfactory` — the name is
overloaded; `/opt/darkfactory/` is also where operator scripts live.

This repo (`~/workspace/x85446/kilroy/`) is kilroy itself.

---

## Architecture — three subsystems

| Subsystem | Role |
|---|---|
| **attractor** | kilroy's planner+executor. `kilroy attractor ingest` reads `docs/*.md` of the target repo and emits an Attractor `.dot` pipeline graph. `kilroy attractor run` executes a `.dot` graph node-by-node. The `attractor` subcommand name is why operator commands say `attractor run`. |
| **CXDB** | Context Database. gRPC (`:9009`) + HTTP (`:9010`) service with a UI (`:9020`). Stores per-turn artifacts — prompts, responses, files, decisions — so each pipeline node reads its predecessors' output and writes its own. CXDB is the only shared state across nodes. |
| **cliproxyapi** | Local LLM gateway on `:8317`. Routes node prompts to Anthropic via an OAuth'd Claude session. Reason: kilroy uses real Claude (not API keys), and one OAuth session fans out to every node. |

---

## How spec becomes pipeline

`kilroy attractor ingest` is itself an agentic LLM run: one Claude agent
reads `docs/*.md` (plus any files those docs reference) and writes an
Attractor `.dot` graph. Nodes look like:

```
node_name [
    llm_provider: anthropic;
    llm_model:    claude-opus-4.7;
    prompt:       "Build the partition-table generator described in stage-1.md.";
    inputs:       [predecessor_node_outputs];
    outputs:      [files_or_artifacts];
];
```

Edges encode "X must finish before Y." Fan-out edges spawn parallel
children that run in sibling worktrees under `parallel/<fanout-name>/pass-<n>/`.

---

## How pipeline becomes product

`kilroy attractor run --graph pipeline.dot`:

```
for each node in topological order:
    read prior context from CXDB
    render prompt with predecessor outputs
    call anthropic via cliproxyapi
    parse response → files + decisions
    write files into worktree, `git commit` per node
    write artifacts to CXDB
    emit progress.ndjson event (node_started, node_completed, …)
```

`run.yaml` is generated per-run (by `kilroyHelp run`) and pins: CXDB
endpoints, the modeldb of pinned model metadata, git policy
(`commit_per_node: true`, `run_branch_prefix: attractor/run`), and runtime
timeouts. CXDB autostart is off because the operator brings CXDB up
explicitly.

---

## Why anthropic-only

cliproxyapi on darkfactory only routes Claude — there's no OpenAI / Gemini
/ DeepSeek upstream wired in. Fresh `pipeline.dot` files from ingest often
contain `llm_provider: openai` or similar (the planner agent doesn't know
about the gateway constraint). `kilroyHelp pipeclean` rewrites every
non-anthropic provider to `anthropic` and picks a tier (`claude-opus-4.7`
for strong / `claude-sonnet-4.6` for the rest) by keyword-matching the
original model name. That step is the seam where a future multi-gateway
host could keep the original providers.

---

## Run artifacts

```
~/.local/state/kilroy/runs/run-<id>/
├── worktree/             ← git worktree on branch attractor/run/run-<id>
│                           (this IS the produced system)
├── progress.ndjson       ← append-only event log (one JSON per line)
├── checkpoint.json       ← resume state (last completed node + model state)
├── run.yaml              ← per-run config (frozen)
├── run.tgz               ← final bundled archive (only on success)
└── parallel/<name>/      ← sibling worktrees from fan-out stages
```

`worktree/.git` is a *file* (62 bytes pointing to a kilroy-managed
commondir), not a directory — many shell checks use `[ -d .git ]` and
silently fail. Prefer `[ -e .git ]`.

---

## Resume semantics

Mid-run interrupts (SSH death, host reboot, kilroy crash, cliproxy outage)
are recovered with `kilroyHelp resume`. Two upstream-bug heals run
automatically inside resume:

1. **Missing `git_commit_sha` in `checkpoint.json`** — kilroy serializes the
   checkpoint *before* the per-node `git commit` event lands. Resume reads
   HEAD from the worktree and injects it.
2. **Stale infra-failure signatures** — cliproxy outages, `auth_failed`,
   `i/o timeout`, `gateway.unhealthy`, etc. get pruned from the
   deterministic cycle detector so a *resolved* outage doesn't make the run
   bail on signature match. Product-bug signatures are left intact.

These heals exist because the bugs are real but not yet fixed upstream.

---

## The triage rule

When something about a run looks wrong, decide first **which** thing is
broken:

| Symptom | Verdict | Action |
|---|---|---|
| kilroy crashed / hung / produced partial output | **kilroy bug** | fix kilroy (this repo). See `AGENTS.md` Prime Directive. |
| kilroy finished cleanly but the produced repo lacks something | **spec bug** | the target repo's `docs/*.md` didn't ask for it. Fix the spec, re-run. |

"Good build, wrong result" → spec bug. "Good intent, broken tool" → kilroy
bug. **Never** patch the produced repo to paper over a kilroy bug; the next
run will regenerate the same broken output. (The list of recurring
kilroy-generated-artifact bugs is in `CLAUDE.md`.)

---

## What kilroy is not

- Not a single-shot codegen tool. It runs as a graph of LLM agents that
  pass context through CXDB.
- Not opinionated about the target — same kilroy builds `izcrOS`
  (a Rust+QEMU bootable OS image) or anything else whose spec lives in
  `docs/*.md`.
- Not autonomous about the spec. It does what `docs/*.md` says. If the spec
  is wrong, the product is wrong. The factory is faithful, not creative.
