# Tessera

Tessera is a single Go binary that runs containers across machines on a LAN. You say what should be running. The controller places it, restarts it, and moves it. You do not assemble Deployments, Services, and probes by hand.

Healing is deterministic. A model is optional, and only for explanation. If the controller dies, a node with the newest signed snapshot can take over. Wipe, reimage, and delete are never done automatically.

What is coming next is in [ROADMAP.md](ROADMAP.md).

## Run

```
go build -o tessera ./cmd/tessera
./tessera up
./tessera apply -f app.yaml
./tessera get apps
./tessera ask "why is web down"
```

`up` listens on `:7468`, writes `~/.tessera/client.json`, and starts a local agent. `--runtime auto` uses the Docker socket if it exists, otherwise `ctr` when containerd is present. Use `--runtime fake` when you have neither. A pulled image is kept on the leader. The next start of that image, on this node or another, does not pull it from the registry again.

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

A Job runs to completion. Set `gang: true` when every replica must land together or not at all. A Model is an App that prefers GPU nodes. A Route is a stable port on the controller. When a move starts a replacement and that replacement is running, the route cuts over. The client keeps the same address.

`cpu` accepts Kubernetes-style quantities (`100m`, `1`). `memory` accepts `128Mi`, `1Gi`, or a byte count.

A release change (image, command, env, configs, secrets) bumps generation. Replica-only changes do not. If the new generation fails and an older one was healthy, Tessera rolls back.

## What it does on its own

The agent heartbeats. After 3 seconds a node is suspect. After 10 seconds it is dead and its work is placed elsewhere. Crash loops restart up to 3 times, then move. A bad release rolls back. A full disk is pruned. A certificate past half its life is renewed. A node that keeps failing workloads is cordoned, then uncordoned after the move cooldown if heartbeats are fresh.

If a faster node appears and the gain is at least 15 percent, a stateless app is started there first. The old copy stops only after the new one is running. Moves wait out a 10 minute cooldown.

If the leader's lease expires, a node holding the snapshot can promote itself. A node that is alone waits 30 seconds, so a short partition does not create a second writer. Agents ignore assignments from an older epoch.

`wipe`, `reimage`, and `delete` are recorded as proposed. Heal does not run them, even if you put them in `auto`. `tessera confirm` lists proposed actions. `tessera confirm <id>` is the only way one runs. Delete removes that app or node. Wipe stops Tessera containers on the node, prunes its runtime, and clears the agent data directory, then removes the node. Reimage does that clear and the agent rejoins empty. Neither wipes the operating system.

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

`tessera get nodes` shows the join benchmark: CPU, memory, and disk. It is recorded once, then refreshed hourly.

`tessera drill` runs the same acceptance suite: placement, dead-node reschedule, rollback, a refused wipe, epoch fencing, restore from the local cache, leader promotion, a move onto a faster node, certificate renewal, route cutover, the image cache, and confirm. It does not need Docker.

## Local Docker lab

The 0.2.0 lab runs a controller and two isolated Docker-backed nodes on one machine. It builds a sample HTTP app, loads it into the first node, applies it through Tessera, and exposes its Route at `http://127.0.0.1:8088/work`. During failover, the second node receives the image through Tessera's leader cache. You need Docker Compose, a running Docker daemon, `curl`, and enough memory for two Docker-in-Docker nodes. The nodes use privileged containers; keep this lab on a machine you control. The full lab drill passed on macOS with OrbStack. Docker Desktop and native Linux host compatibility has not been verified yet.

```sh
sh lab/lab.sh up
sh lab/lab.sh status
sh lab/lab.sh load smoke
sh lab/lab.sh scenario crash
sh lab/lab.sh scenario node-loss
sh lab/lab.sh down
```

`load` accepts `smoke`, `baseline`, `spike`, `soak`, or `failure`. The k6 thresholds check HTTP errors and p95 latency. JSON summaries go to `lab/results/`. `scenario latency` injects 750 ms of delay and verifies the baseline latency threshold fails; `scenario errors` returns a 503 on every second request and verifies k6 sees failures. `reset` clears either fault. `scenario node-loss` stops the active node, times Route recovery on the other node, and prints whether to run `recover a` or `recover b`. `scenario confirm-delete` proposes and confirms removal of the sample app, then reapplies it.

Run `sh lab/lab.sh services` to add a local registry, PostgreSQL, Redis, and S3-compatible object store. They listen on local ports 5001, 55432, 16379, and 19000 (console 19001). The PostgreSQL user and database are `lab`; its password and the object store's `lab` credentials are `lab-only-password`. Tessera workloads can resolve `registry`, `postgres`, `redis`, and `object-store` on either isolated node; the sample app verifies all four at `http://127.0.0.1:8088/dependencies`. A lab DNS relay runs beside each node, and `TESSERA_LAB_BRIDGE_IP` can change its default `172.30.250.1` bridge address if that subnet conflicts with a local network. The basic lab needs no cloud account. `sh lab/lab.sh load-image IMAGE` copies a local image to both nodes, and `sh lab/lab.sh apply MANIFEST` submits your own manifest. `down` keeps lab volumes; `clean` removes the lab containers and volumes.

Run `sh lab/lab.sh verify` to execute the complete sample evaluation from a clean lab. It checks optional services by name and verifies the second node has no sample image before failover and does have it afterward. It removes the lab's containers and volumes after the run; saved k6 summaries remain in `lab/results/`.

The lab exercises Tessera scheduling, recovery, routing, and load handling with local containers. It does not reproduce provider networking, IAM, managed database behavior, autoscaling, or cloud billing.

On macOS, use a Docker Desktop or OrbStack Linux engine with Compose v2. On Linux, use Docker Engine with Compose v2 and a user account allowed to access its daemon. The lab needs a shell, `curl`, available ports 7468, 8088, 5001, 55432, 16379, 19000, and 19001, and support for privileged Docker-in-Docker containers. The optional service ports bind to localhost only. Each isolated node uses `172.30.250.0/24` by default for its inner bridge; set `TESSERA_LAB_BRIDGE_IP` before starting the lab if that subnet overlaps a local route. The lab's DNS relay forwards service-name lookups into the Compose network. Real cloud DNS, networking, credentials, and managed service behavior still need testing in the intended environment.

## License

Copyright 2026 Clarke.

Tessera is licensed under the Apache License, Version 2.0. See `LICENSE`.

Use, modify, and contribute. A company can ship Tessera, including inside a product, without a special agreement. Contributions are licensed the same way, and each contributor grants a patent license for their contribution.
