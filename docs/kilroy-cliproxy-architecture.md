# kilroy ↔ cliproxyapi architecture

How kilroy reaches LLM providers, how cliproxyapi fronts them, and how the
usage-gate controls the flow.

## The path today

One path, Anthropic-only. `kilroy-run.service` on darkfactory exports a single
relevant variable:

```
ANTHROPIC_BASE_URL=http://127.0.0.1:8317
```

kilroy's Anthropic adapter sends `anthropic_messages` requests to cliproxyapi on
`:8317`, which forwards them upstream using the stored `claude-*.json` OAuth
credential. No `OPENAI_BASE_URL` is set and no graph node selects an OpenAI
model, so the `codex-*.json` credential that also lives in the proxy is never
exercised by a run.

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

Pooling a Claude model and a GPT model under a single client-facing alias (so one
endpoint round-robins across both AIs — Mission 1A) relies on the alias-pool plus
translation layer; it is configured in cliproxyapi, not in kilroy.

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

`kilroyHelp gate run` watches account usage at each safe stage boundary
(`node.completed`) and parks/​resumes the run to stay inside a budget. The gate,
the proxy credentials, and kilroy's model selection are the three levers that
together decide which AI does the next unit of work and when the run pauses.
