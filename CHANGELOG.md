# Changelog

## 0.1.1

Roadmap only. No runtime change.

- Version order is priority order. Next is the CLI session, then the real-machine path, the Kubernetes bridge, and controller replication. Inference, training, public images, cloud, and infrastructure-as-code follow. Netboot stays unscheduled.
- Direct modules stay on the known set: `gopkg.in/yaml.v3`, `github.com/hashicorp/mdns`, `github.com/modelcontextprotocol/go-sdk`, `golang.org/x/sys`, and `modernc.org/sqlite`. A new one is a roadmap change.

## 0.1.0

First cut of the Tessera binary.

- Controller, agent, and CLI in one binary. State is a single SQLite file.
- User resources: App, Job, Model, Route, Config, Secret, Policy.
- Scheduler places by capacity, measured free memory, GPU count, and node score. Gang jobs are all or nothing. Dependencies are held until the dependency is running.
- Faster-node moves are make-before-break, with a minimum gain and a cooldown.
- Deterministic healing: restart, reschedule, cordon, rollback, prune, and certificate renewal. Wipe, reimage, and delete stay proposed.
- Leader loss promotes a node from a signed snapshot. Agents reject an older epoch. The local assignment cache restores work with no controller.
- Watchdog around `tessera up`. mDNS join for an agent that already has the token.
- Docker runtime, a `ctr` path, and a fake runtime. Drills use fake.
- Read-only MCP tools and `tessera ask`. Optional LLM note via `TESSERA_LLM_URL`.
- Kubernetes manifest import, including `--from-cluster` through `kubectl`.
- Licensed under the Apache License, Version 2.0.
