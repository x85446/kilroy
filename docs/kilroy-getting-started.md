# Kilroy getting-started — running a session on darkfactory

"I forgot the steps again" reference. Worked example: `~/workspace/izuma/izcrOS`.

Companion docs: `CLAUDE.md`, `docs/runs-layout.md`, `docs/resume.md`.

---

## The loop

```bash
ssh darkfactory
git clone git@github.com:IzumaNetworks/izcrOS.git ~/work/izcrOS && cd ~/work/izcrOS

kilroyHelp cxdb-start         # CXDB up
kilroyHelp ingest             # docs/*.md  →  pipeline.dot
kilroyHelp pipeclean          # force every node to anthropic
kilroyHelp launch run         # detached run via systemd (survives SSH disconnect)
kilroyHelp launch logs        # tail journal

cd "$(kilroyHelp results cdlatest)"   # inspect the produced worktree
```

Foreground variant: `kilroyHelp run` instead of `launch run` (closing SSH kills it).

---

## Resume

```bash
kilroyHelp resume                     # last run
kilroyHelp resume --list              # pick one
kilroyHelp launch resume              # detached
```

Auto-heals `checkpoint.json` missing `git_commit_sha` and prunes stale
infra-failure signatures (cliproxy outages, auth timeouts).

---

## Cleanup

| Goal | Command |
|---|---|
| Full reset (no-run-ever) | `kilroyHelp wipe` |
| Free disk mid-run | `kilroyHelp clean-active [--tier2\|--all] [--pretend]` |
| Snapshot a file on every change | `kilroyHelp catchpipe ./pipeline.dot` |

---

## Troubleshooting

**`cliproxy-status.sh` → 503 / `auth_unavailable`** — OAuth access token expired
and cli-proxy-api 7.x doesn't bootstrap-refresh. From the workstation:
```bash
cd ~/workspace/x85446/creds && ./darkfactorySetup.sh
```
(pulls fresh tokens from `darkfactoryold`; if that VM is stopped,
`incus start IncusOS:darkfactoryold` first.)

**`kilroyHelp run` → "CXDB at 127.0.0.1:9010 did not respond"** —
`kilroyHelp cxdb-start`.

**`kilroyHelp ingest` → "no docs/*.md files found"** — either wrong cwd, or
pass requirements explicitly: `kilroyHelp ingest "<text>"`.

---

## Paths

| What | Where |
|---|---|
| dispatcher (master) | `~/workspace/x85446/creds/kilroyHelp` (workstation) |
| dispatcher (deployed) | `/opt/darkfactory/bin/kilroyHelp` (darkfactory) |
| run artifacts | `~/.local/state/kilroy/runs/run-<id>/` |
| last-run pointer | `~/.local/state/kilroy/runs/last-run` |
| systemd unit / env | `/etc/systemd/system/kilroy-run.service`, `/etc/kilroy-run.env` |
| CXDB HTTP / UI | `http://127.0.0.1:9010`, `http://127.0.0.1:9020` |
| cliproxy gateway | `http://127.0.0.1:8317` |
| OAuth tokens | `~/.cli-proxy-api/auth/claude-*.json` |
