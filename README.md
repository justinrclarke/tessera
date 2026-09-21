# Tessera

Tessera is a single Go binary that runs containers across machines on a LAN. You say what should be running. The controller places it, restarts it, and moves it. You do not assemble Deployments, Services, and probes by hand.

Healing is deterministic. A model is optional, and only for explanation. If the controller dies, a node with the newest signed snapshot can take over. Wipe, reimage, and delete are never done automatically.

## Run

```
go build -o tessera ./cmd/tessera
./tessera up
./tessera apply -f app.yaml
./tessera get apps
./tessera ask "why is web down"
```

`up` listens on `:7468`, writes `~/.tessera/client.json`, and starts a local agent. `--runtime auto` uses the Docker socket if it exists, otherwise `ctr` when containerd is present. Use `--runtime fake` when you have neither.

Another machine that already has the binary and the cluster token joins without an address:

```
tessera agent --token "$(cat ~/.tessera/token)"
```

The controller advertises `_tessera._tcp`. Copy the token once. After that, discovery is mDNS.

State lives in `$TESSERA_DATA` or `~/.tessera`. Override the client with `TESSERA_URL` and `TESSERA_TOKEN`.

## What you write

```yaml
kind: App
name: web
image: nginx:alpine
replicas: 2
sensitive_to: cpu
ports:
  - container: 80
    host: 8080
resources:
  cpu: 100m
  memory: 128Mi
```

Kinds: App, Job, Model, Route, Config, Secret, Policy. Separate documents with `---`.

A Job runs to completion. Set `gang: true` when every replica must land together or not at all. A Model is an App that prefers GPU nodes. A Route is a stable port on the controller that follows the current assignment.

`cpu` accepts Kubernetes-style quantities (`100m`, `1`). `memory` accepts `128Mi`, `1Gi`, or a byte count.

A release change (image, command, env, configs, secrets) bumps generation. Replica-only changes do not. If the new generation fails and an older one was healthy, Tessera rolls back.

## What it does on its own

The agent heartbeats. After 3 seconds a node is suspect. After 10 seconds it is dead and its work is placed elsewhere. Crash loops restart up to 3 times, then move. A bad release rolls back. A full disk is pruned. A certificate past half its life is renewed. A node that keeps failing workloads is cordoned, then uncordoned after the move cooldown if heartbeats are fresh.

If a faster node appears and the gain is at least 15 percent, a stateless app is started there first. The old copy stops only after the new one is running. Moves wait out a 10 minute cooldown.

If the leader's lease expires, a node holding the snapshot can promote itself. A node that is alone waits 30 seconds, so a short partition does not create a second writer. Agents ignore assignments from an older epoch.

`wipe`, `reimage`, and `delete` are recorded as proposed. They are not executed, even if you put them in `auto`.

## Ask, and MCP

`tessera ask` answers from findings and the action log. If `TESSERA_LLM_URL` and `TESSERA_LLM_KEY` are set, a short note is appended. The model is not required, and it is not on the heal path.

`tessera mcp` speaks MCP over stdio. Tools are read-only: `list_apps`, `get_app`, `list_nodes`, `get_node`, `logs`, `diagnose`, `list_actions`, `explain`, `metrics`.

## Kubernetes

`tessera import -f deploy.yaml` converts Deployment, StatefulSet, DaemonSet, Job, Service, Ingress, ConfigMap, and Secret. Other kinds are printed as skipped. `--from-cluster` shells out to `kubectl`. This is a conversion, not a Kubernetes API.

```
tessera import -f deploy.yaml --dry-run
tessera import --from-cluster
```

## Boot and backup

`tessera install` writes a launchd agent or a systemd user unit. It does not load it. `tessera backup -o tessera-backup.db` copies the SQLite store.

`tessera up` restarts itself through a watchdog unless you pass `--watched` or set `TESSERA_WATCHED=1`. A clean exit is not restarted. A child that dies within 500ms is not restarted either.

## Check

```
go test ./...
go test ./internal/drill -count=1
```

`tessera drill` runs the same acceptance suite: placement, dead-node reschedule, rollback, a refused wipe, epoch fencing, restore from the local cache, leader promotion, a move onto a faster node, and certificate renewal. It does not need Docker.

## License

Copyright 2026 Clarke.

Tessera is licensed under the Apache License, Version 2.0. See `LICENSE`.

Use, modify, and contribute. A company can ship Tessera, including inside a product, without a special agreement. Contributions are licensed the same way, and each contributor grants a patent license for their contribution.
