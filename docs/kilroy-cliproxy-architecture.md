# kilroy ↔ cliproxyapi architecture

How kilroy reaches LLM providers, how cliproxyapi fronts them, and how the
usage-gate controls the flow.

## The path

kilroy reaches both Claude and OpenAI through a single cliproxyapi endpoint that
round-robins between them. `kilroy-run.service` exports:

```
ANTHROPIC_BASE_URL=http://127.0.0.1:8317
```

kilroy's Anthropic adapter sends `anthropic_messages` requests to cliproxyapi on
`:8317`. A fresh run (`kilroyHelp run`) forces every node to the model alias
`auto` (`--force-model anthropic=auto`); cliproxyapi resolves `auto` through a
self-referential `openai-compatibility` pool that round-robins `claude-opus-4-8`
(served by `claude-*.json`) and `gpt-5.5` (served by `codex-*.json`), translating
protocols so the Anthropic-protocol client transparently gets either upstream.

The usage-gate keeps each provider inside its budget independently: a provider
over budget is dropped from the pool (its auth `disabled`), so the pool fails
over to the other; the run pauses only when **both** are gated.

A resumed run keeps the models baked into its original graph (`resume` does not
accept `--force-model`), so to put an existing run on the pool, start it fresh.

## kilroy side — provider-aware by design

kilroy picks a provider and model per node, not per run:

- **Adapters** are separate per provider, each reading its own base-URL env var:
  - `internal/llm/providers/anthropic/adapter.go` — `ANTHROPIC_BASE_URL`,
    default `https://api.anthropic.com`, `anthropic_messages` protocol.
  - `internal/llm/providers/openai/adapter.go` — `OPENAI_BASE_URL`, default
    `https://api.openai.com`, OpenAI chat-completions protocol.
  - Base-URL overrides resolve in
    `internal/attractor/engine/api_client_from_runtime.go`
    (`resolveBuiltInBaseURLOverride`).
- **Model selection** flows: DOT graph `model_stylesheet` / per-node
  `llm_provider` + `llm_model` attrs → optional `--force-model provider=model`
  CLI overrides → resolved per node in
  `internal/attractor/engine/agent_router.go` (`AgentRouter.Run`, emits a
  `provider_selected` progress event).
- The resolved set is recorded in the run's `manifest.json` under `force_models`
  (`internal/attractor/engine/engine.go`).

Because provider is a per-node property with a per-provider base URL and wire
protocol, kilroy can natively split work across Claude and OpenAI (Mission 1B).
It does not, on its own, blend two providers behind one model name.

## cliproxyapi side — one endpoint, many credentials

cliproxyapi (`/etc/cliproxyapi.conf`, port 8317) presents an OpenAI- and
Anthropic-compatible surface and routes each request by **model name**:

- A request for a `claude-*` model uses a `claude-*.json` credential; a request
  for a `gpt-*` model uses a `codex-*.json` credential.
- `routing.strategy: round-robin` round-robins **only among credentials that
  serve the same model** — e.g. two Claude accounts for one Claude model. It
  does not, by default, alternate between a Claude model and a GPT model.
- A translation layer lets a client speak one protocol while the upstream speaks
  another, and `model-aliases` / per-provider `models:` pools map client-facing
  aliases onto upstream models.
- Per-credential `disabled` plus cooldown/`quota-exceeded` switching let the
  proxy drop a credential from rotation and fail over to another.

### The dual-AI pool

`/etc/cliproxyapi.conf` defines an `openai-compatibility` provider that points
back at the proxy itself and pools the two upstream models under one alias:

```yaml
openai-compatibility:
  - name: dualpool
    base-url: http://127.0.0.1:8317/v1
    api-key-entries:
      - api-key: dummy-key
    models:
      - name: claude-opus-4-8
        alias: auto
      - name: gpt-5.5
        alias: auto
```

A repeated alias builds a round-robin pool with failover: a request for `auto`
alternates between `claude-opus-4-8` and `gpt-5.5`; if the chosen upstream is
unavailable (its OAuth auth is `disabled`), the request continues on the other.
The inner calls use concrete model names, so there is no routing loop. (Overlap­
ping the same alias across raw OAuth channels via `oauth-model-alias` does NOT
work — it resolves ambiguously; the `openai-compatibility` pool is the mechanism.)

## Auth files

Each logged-in upstream is one `<type>-<email>.json` under `~/.cli-proxy-api/`:

| field | meaning |
|---|---|
| `type` | `claude` \| `codex` |
| `email` | account |
| `disabled` | drop this credential from rotation when `true` |
| `expired` | access-token expiry (the proxy auto-refreshes) |
| `last_refresh` | last successful refresh |

`kilroyHelp cliproxy login [claude\|codex]` creates/renews these; `kilroyHelp
status` reports one `auth <type>` line per credential with its freshness.

## Usage-gate

`kilroyHelp gate run` controls both providers from one config,
`/etc/kilroy-usage-gate.conf` (`PROVIDERS=claude codex`, re-read every tick). Each
provider has the same 5h/7d burn envelope, evaluated against its own usage:

- **Claude** — Anthropic `/api/oauth/usage` (`five_hour`/`seven_day` utilization).
- **Codex** — ChatGPT `backend-api/codex/usage` (`primary_window` = 5h,
  `secondary_window` = 7d, `used_percent`). Same shape, same thresholds.

Each tick the gate probes both, enforces the active routing strategy (below), and
pauses the run (`launch stopsafe`) only when **every** provider is gated, resuming
(`launch resume`) the moment one frees. `kilroyHelp usage --provider
claude|codex|both` shows live utilization; `kilroyHelp gate --check` shows the
per-provider + run verdict; `kilroyHelp gate --tick` runs one enforcement pass;
`kilroyHelp status` shows each provider's gate state and the serving engine.

While a provider is gated, `kilroyHelp status`/`cliproxy check` reports `claude
-p: skipped (claude gated)` rather than a failure — a deliberately out-of-pool
provider is not a health fault.

## Routing strategies

`STRATEGY` in `/etc/kilroy-usage-gate.conf` is an ordered **degradation ladder**.
Each tick the gate picks the first rung the currently-healthy providers satisfy;
a rung change does `stopsafe → re-route → resume`. The strategy also picks the
**family**, which determines how kilroy and the gate route:

| Family | When | kilroy launch | gate lever |
|---|---|---|---|
| **graph** | `STRATEGY` contains `role-split` | direct per-node providers — both `ANTHROPIC_BASE_URL` + `OPENAI_BASE_URL` (bare host, no `/v1`), `run.yaml` has an `openai` provider, graph pipecleaned per-role | re-pipeclean the run's graph; **auths stay enabled** |
| **pool** | otherwise (`round-robin` / `fill-first`) | the `auto` pool (`--force-model anthropic=auto`) | per-credential `disabled` (pool membership) |

Rungs:

- **role-split** (needs BOTH engines) — each node runs on its role's engine:
  strong/coding nodes (`PRIMARY` / `ROLE_STRONG_PROVIDER`) on one engine, default
  nodes (`ROLE_DEFAULT_PROVIDER`) on the other. Claude nodes call `/v1/messages`;
  OpenAI nodes call `/v1/responses` (the OpenAI Responses API, which cliproxyapi
  routes to codex). `kilroyHelp pipeclean --mode role-split` writes the per-class
  assignment.
- **fill-first** (needs ≥1) — all nodes on `PRIMARY` while it is healthy, else all
  on the survivor. Graph family pipecleans `--mode to-engine`; pool family keeps
  only the survivor's auth in the pool.
- **round-robin** (needs ≥1) — nodes use the `auto` pool; the gate's auth-disable
  makes the pool serve whichever provider is healthy.

The default ladder is `role-split fill-first`: role-split when both engines are
healthy; when one gates, pipeclean its nodes onto the survivor (fill-first); on
recovery, pipeclean back to role-split; pause only when both gate. To survive a
fill-first flatten, the gate always re-derives the rewrite from a pristine
snapshot (`graph.dot.orig`) that retains the original per-role assignment, so the
role information is never lost.
