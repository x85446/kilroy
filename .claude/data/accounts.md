# Known Access

## Used by this project

- **darkfactory** — kind: ssh, source: ~/.ssh/config.d/cypress, role: kilroy dev/test VM at 10.0.171.10 (Ubuntu 26.04, IncusOS VM) — deploy target for kilroyHelp, runs CXDB + cliproxyapi + the kilroy attractor binary
- **fieldstone** — kind: ssh, source: ~/.ssh/config.d/cypress, role: SSH proxyjump for darkfactory and kilroyfactor — all incoming connections from outside the 10.0.0.0/16 network land here first
