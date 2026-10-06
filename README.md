# Tessera

Tessera is a single Go binary that runs containers across machines on a LAN. You say what should be running. The controller places it, restarts it, and moves it. You do not assemble Deployments, Services, and probes by hand.

Healing is deterministic. A model is optional, and only for explanation. If a standalone controller dies, a node with the newest signed snapshot can take over. With three controller replicas, a majority elects the replacement and agents reconnect without restarting running work. Wipe, reimage, and delete are never done automatically.

What is coming next is in [ROADMAP.md](ROADMAP.md).

## Run

Published binaries are available for each OS and architecture from the [latest release](https://github.com/justinrclarke/tessera/releases/latest), with SHA-256 checksums. The unreleased 0.4.0 commands and fixes described here require building this branch:

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

Run `tessera` or `tessera session` to open a session against your configured controller. Enter `get apps`, `apply -f app.yaml`, `import -f deploy.yaml`, or another CLI command to run it. Any other line is a question, answered through the same deterministic diagnosis as `ask`. Commands accept quoted paths, such as `apply -f "my app.yaml"`, and an optional `tessera` prefix. Errors leave the session open. Type `exit` or `quit`, or close standard input, to leave the cluster running. Start long-running processes such as `up`, `agent`, `gateway`, and `mcp` in a separate terminal; use file paths for manifests inside a session.

## What you write

Save this as `app.yaml`. Docker assigns separate workload ports so both replicas can run on one node; the Route gives clients a fixed port:

```yaml
kind: App
name: web
image: nginx:alpine
replicas: 2
sensitive_to: cpu
ports:
  - container: 80
health:
  path: /
  port: 80
  timeout: 2s
  startup_timeout: 60s
resources:
  cpu: 100m
  memory: 128Mi
---
kind: Route
name: web
app: web
port: 8080
target_port: 80
```

With `tessera up` running in another terminal, apply the file and check the workload:

```sh
./tessera apply -f app.yaml
./tessera get apps
./tessera get controllers
curl --retry 10 --retry-all-errors --retry-delay 1 http://127.0.0.1:8080/
```

Use Docker for this HTTP trial. `--runtime fake` exercises the control plane and does not serve the container's HTTP endpoint. `READY` counts assignments passing the configured HTTP readiness check; without `health`, it counts running processes. Unknown App fields and invalid probe settings fail explicitly. Stop the trial workload by changing `replicas` to `0`, applying the file again, and checking `tessera get assignments` for stopped assignments before exiting `up`. Exiting a CLI session leaves workloads running.

Run one Tessera agent per Docker daemon. Its runtime manages containers with the `tessera_` prefix; a second agent pointing at the same daemon can stop the first agent's workloads. The Docker lab gives each node its own daemon. Use disposable stateless workloads for the trial; persistent volumes and stateful identity are not implemented.

Kinds: App, Job, Model, Route, Config, Secret, Policy. Separate documents with `---`.

A Job runs to completion. Set `gang: true` when every replica must land together or not at all. A Model is an App that prefers GPU nodes. A Route listens on a fixed controller port and balances new TCP connections across ready replicas. If a backend cannot be reached, it tries another before forwarding any bytes. Updates and moves become eligible for routing only after readiness. Use the independent gateway below when the client address must survive loss of a controller.

Start an agent with `--labels fabric=ethernet-a,storage=shared` to describe its host. An App or Job can require `node_labels: {storage: shared}`. For a gang Job, `gang_fabric: fabric` keeps all workers on nodes with the same `fabric` value. Labels describe a real link or shared mount; Tessera does not create either one.

`cpu` accepts Kubernetes-style quantities (`100m`, `1`). `memory` accepts `128Mi`, `1Gi`, or a byte count.

For an NVIDIA model server, set `kind: Model`, `gpus: 1`, and optionally `gpu_model: NVIDIA H100` and `gpu_memory: 20Gi`. The agent reads model and free memory through `nvidia-smi`; Tessera reserves a GPU UUID for each replica and asks Docker to expose that device. The host needs an NVIDIA driver and the NVIDIA Container Toolkit. GPU memory is checked at placement time and is not a container memory limit. The `ctr` runtime currently rejects GPU workloads. Attach a Route to the Model name as you would for an App.

A release change (image, command, environment, ports, resources, health, placement constraints, or dependencies) bumps generation. Referenced Config and Secret data changes also create a generation; Tessera saves the resolved environment with that release. Replica-only changes do not. Rollback restores the previous workload's values without changing the shared Config or Secret object. A rejected multi-document apply leaves existing resources unchanged.

For Apps, Tessera starts one replacement at a time and keeps the old assignment until the replacement is ready. A generation becomes healthy only when all desired replicas are ready. A failed generation rolls back when a prior healthy generation exists. Spare resources are required; Tessera holds the old workload when no replacement fits. Fixed host ports require a different available node. Jobs retain their cancel-and-replace behavior on a generation change.

`health` checks an HTTP path on the single published container port. Only 2xx responses pass; redirects do not. `timeout` defaults to `2s` per request and `startup_timeout` to `60s`. Until readiness passes, the assignment is `starting` and Routes exclude it. Startup timeout stops the failed replacement and allows rollback. Later readiness failure removes that replica from routing while leaving its process running and checking for recovery; this is not a liveness restart policy. Probe timing survives agent restart. The verified rollout path uses Docker; containerd host-network port placement still needs acceptance. Existing TCP connections to a removed backend may close during cutover; connection draining remains open.

## Stable service access

Run the gateway on a host that stays available independently of the controllers, using the cluster token and all three controller addresses:

```sh
export TESSERA_URL=http://controller-a:7468,http://controller-b:7468,http://controller-c:7468
export TESSERA_TOKEN=your-cluster-token
./tessera gateway --route web --port 8080
```

Clients use `http://gateway-host:8080/`. The gateway refreshes ready backend addresses once per second and connects directly to the nodes. It keeps its last accepted backend set during controller outages, so an election or loss of quorum does not stop existing traffic. An authoritative missing Route or authorization failure clears that set. It rejects older controller epochs. A first start requires a reachable controller and an existing Route; a restart cannot recover cached routing while all controllers are unavailable.

The listen port binds on all interfaces. On the same host as a controller, choose a different port, such as `--port 18080`, to avoid conflicting with its Route listener. The gateway requires network access to published node ports. It balances TCP connections, does not replay application requests, and may close connections when their backend is removed. Run it in a separate terminal; no gateway service installation or automatic failover of the gateway itself is implemented. High availability of that address still needs an external load balancer or floating-address arrangement. Controller loss and gateway-host loss are separate acceptance cases.

An App can include a `build` stanza instead of an `image`. `tessera apply -f app.yaml` builds it with the local Docker CLI before applying the resulting image. The build file names a base image, optional `apk` or `apt` packages, a process command, and a repository. Tessera copies the selected context into `/app`. With `push: true`, Docker uses credentials already configured on the operator's machine and Tessera applies the registry digest. Without push, it applies a local image-ID tag, which is useful only when the controller and nodes can obtain that image through their local runtime or Tessera's image cache.

```yaml
kind: App
name: web
build:
  base: python:3.13-alpine
  repository: registry.example.com/team/web
  context: ./web
  package_manager: apk
  packages: [curl]
  command: [python, app.py]
  push: true
ports:
  - container: 8080
```

Set `push: false` to keep the image local. The pushed App image is pinned to the digest reported by the registry. Changes to the base tag or package repository between builds can produce different image IDs; pin the base image and package versions when repeatability matters.

## What it does on its own

The agent heartbeats. After 3 seconds a node is suspect. After 10 seconds it is dead and its work is placed elsewhere. Crash loops restart up to 3 times, then move. A bad release rolls back. A full disk is pruned. A certificate past half its life is renewed. A node that keeps failing workloads is cordoned, then uncordoned after the move cooldown if heartbeats are fresh.

If a faster node appears and the gain is at least 15 percent, a stateless app is started there first. The old copy stops only after the new one is running. Moves wait out a 10 minute cooldown.

If the leader's lease expires, a node holding the snapshot can promote itself. A node that is alone waits 30 seconds, so a short partition does not create a second writer. Agents ignore assignments from an older epoch.

`wipe`, `reimage`, and `delete` are recorded as proposed. Heal does not run them, even if you put them in `auto`. `tessera confirm` lists proposed actions. `tessera confirm <id>` is the only way one runs. Delete removes that app or node. Wipe stops Tessera containers on the node, prunes its runtime, and clears the agent data directory, then removes the node. Reimage does that clear and the agent rejoins empty. Neither wipes the operating system.

## Ask, and MCP

`tessera ask` answers from findings and the action log. If `TESSERA_LLM_URL` and `TESSERA_LLM_KEY` are set, a short note is appended. The model is not required, and it is not on the heal path.

`tessera mcp` speaks MCP over stdio. Tools are read-only: `list_apps`, `get_app`, `list_nodes`, `get_node`, `logs`, `diagnose`, `list_actions`, `explain`, `metrics`.

## Kubernetes

`tessera import -f deploy.yaml` converts Deployment, StatefulSet, DaemonSet, Job, Service, Ingress, ConfigMap, and Secret. Other kinds are printed as skipped. `--from-cluster` shells out to `kubectl`, includes StatefulSets and DaemonSets, and reports unsupported kinds found by `kubectl get all`, custom resource definitions, and webhook configurations. It reports an inspection warning if access to either inventory is denied. Custom resource instances are not converted. This is a conversion, not a Kubernetes API.

```
tessera import -f deploy.yaml --dry-run
tessera import --from-cluster --kubeconfig /path/to/kubeconfig --namespace demo --dry-run
tessera ask --kubeconfig /path/to/kubeconfig --namespace demo "why is web down?"
tessera mcp --kubeconfig /path/to/kubeconfig --namespace demo
```

`ask` and `mcp` use the same read-only tool names against the selected Kubernetes cluster. They inspect Deployments, StatefulSets, DaemonSets, Jobs, Pods, nodes, and warning events. Use `--context NAME` to select a context, or `--kubernetes` to use the current `kubectl` context. These flags select the Kubernetes bridge instead of the Tessera controller. Workloads in different namespaces need `namespace/name` for `get_app` and `logs`. The bridge does not mutate Kubernetes objects or record Tessera actions. Your kubeconfig needs read access to the inspected resources.

Import skips and reports workloads requiring storage volumes, sidecars, init containers, health probes, security context, service account settings, or environment references rather than dropping those requirements. Review `--dry-run` output and every skipped report before applying. Persistent application data and stateful identity are not migrated. Service selector mapping, namespace collisions, per-node DaemonSet placement, and multiple target ports still need acceptance; the current supported trial is a simple stateless workload with one TCP port.

`sh lab/kubernetes.sh` runs the 0.3.0 handoff drill with an isolated kind cluster and kubeconfig. It diagnoses a failed image pull, converts a Deployment and Service, applies them to the local Tessera lab, checks the resulting Route, and removes its test clusters. It requires `kind`, `kubectl`, Docker Compose, Go, and `curl`, and refuses to replace an already running Tessera lab.

## Boot and backup

`tessera install` copies the running binary to `~/.local/bin/tessera`, writes a launchd agent on macOS or a systemd user unit on Linux, and starts it. The service runs `tessera agent` against an existing controller. Supply `--url` and `--token`, or use the saved controller configuration. With only a token, the agent discovers the controller over mDNS and saves its address for subsequent CLI commands. The token stays in private files in the data directory. `--runtime` and `--labels` configure the installed node, `--bin-dir` changes the binary destination, and `--data` changes the cluster data directory. Use the same `TESSERA_DATA` directory for subsequent CLI commands. Add the binary directory to your shell's `PATH` if needed.

```sh
./tessera install --url http://controller:7468 --token "$TESSERA_TOKEN"
~/.local/bin/tessera
```

For the first machine, run `tessera controller` in one terminal, then `tessera install` in another to start its agent from the saved configuration. `tessera up` remains the combined controller and agent path for foreground use. Keep one agent process per node data directory. Re-running install replaces the binary atomically and restarts the user service. `--no-start` writes the files without changing the running service; omit it on a subsequent install to activate the service. Service startup errors are reported even when the files were installed successfully. macOS writes agent output to `DATA_DIRECTORY/agent.log`; Linux uses the user journal. These are user services and follow the platform's user-session lifecycle.

`tessera backup -o tessera-backup.db` copies the local SQLite store through a read-only connection while the controller may be running. For an offline replacement controller, run `tessera restore -f tessera-backup.db --data NEW_DIRECTORY` before starting it. Restore checks the backup, refuses an existing database, preserves the cluster token and committed data, assigns a fresh controller identity, and advances the leader epoch. A replica backup also clears old consensus metadata and logs. `--epoch N` sets a higher minimum epoch when agents have seen a later leader. Stop or fence every previous controller before starting the replacement; image cache files are separate from the database backup.

## Three controller replicas

Start a new cluster on three machines with empty controller data directories, stable controller IDs, and the same `TESSERA_TOKEN`. Give every process the same peer list. Each peer entry is `ID=RAFT_ADDRESS@HTTP_URL`; the addresses must be reachable by the other controllers and agents. Replication uses mutual TLS authenticated by the shared token. The HTTP API uses the bearer token; use the trusted LAN or an HTTPS reverse proxy for it.

Set this peer list on each machine, replacing the hostnames with your own:

```sh
PEERS='a=controller-a:7469@http://controller-a:7468,b=controller-b:7469@http://controller-b:7468,c=controller-c:7469@http://controller-c:7468'
```

On controller A:

```sh
tessera controller --id a --listen :7468 --raft-listen :7469 --data ~/.tessera-a --peers "$PEERS" --bootstrap
```

On controller B and controller C, respectively:

```sh
tessera controller --id b --listen :7468 --raft-listen :7469 --data ~/.tessera-b --peers "$PEERS"
tessera controller --id c --listen :7468 --raft-listen :7469 --data ~/.tessera-c --peers "$PEERS"
```

Use `--bootstrap` on A for the first start. Restart each controller with its original ID, directory, token, and peer list. A stored replica database requires the replica configuration when reopened. Membership changes and converting an existing standalone database into replicas are not supported.

Agents and CLI clients can start with one listed HTTP URL and learn the others, or take all three URLs separated by commas to tolerate an unavailable initial controller:

```sh
tessera install --url http://controller-a:7468,http://controller-b:7468,http://controller-c:7468 --token "$TESSERA_TOKEN"
TESSERA_URL=http://controller-a:7468,http://controller-b:7468,http://controller-c:7468 tessera get apps
```

Two controllers are required to commit changes. If a majority is unreachable, existing containers continue running and agents keep reconnecting; they do not create another standalone leader. Controller state and release history are replicated. Image cache files stay on each controller, so images needed after failover must also be available from their registry or the node runtime. Route ports remain local to each controller. The independent gateway keeps a separate client address during controller loss; it does not provide a floating address if the gateway host fails. The acceptance drill and TCP/TLS restart tests run locally without Docker; acceptance on three physical machines remains open.

`tessera get controllers` inspects each configured address directly, including followers and an isolated controller. It shows role, write availability, fencing epoch, and committed and applied log indexes, and reports unreachable addresses. A healthy HTTP process can still be unable to commit because it lacks a majority. The status endpoint requires the cluster token. Successful operator request receipts survive elections for 24 hours; registration, heartbeat, and status receipts have a two-minute retry window. Do not manually replay old mutation IDs as a permanent deduplication mechanism.

The process acceptance test starts three CLI controllers with private temporary stores and real TCP/TLS replication, kills a leader abruptly, removes quorum, and recovers quorum from a restarted replica without restarting the workload. It continuously sends HTTP requests through an independent gateway at one stable address during leader and quorum loss. Run it with `go test ./cmd/tessera -run TestControllerProcesses -count=1`. It complements the full snapshot, log, partition, and offline-recovery tests; it does not establish cross-machine networking or gateway-host failover.

Databases, sidecars, and backups contain cluster credentials and Secret values and use private file permissions. Raft snapshot directories are private. Keep application-data backups separate: `tessera backup` copies control-plane state and does not back up volumes or container filesystems. See the trial and reliability gates in [ROADMAP.md](ROADMAP.md) for remaining installation, upgrade, gateway availability, access-control, and capacity gates. The default drill verifies HTTP service through slow startup and invalid-image rollback. With a cached `busybox:latest`, `TESSERA_DOCKER_SMOKE=1 go test ./internal/drill -run TestDockerReadinessRolloutSmoke -count=1 -v` verifies actual Docker startup, ready cutover, and failed-command rollback using only containers created by that test.

`tessera up` restarts itself through a watchdog unless you pass `--watched` or set `TESSERA_WATCHED=1`. A clean exit is not restarted. A child that dies within 500ms is not restarted either.

## Cloud inventory

`tessera cloud inventory --provider aws --region us-east-1`, `--provider gcp --project PROJECT`, or `--provider azure --subscription SUBSCRIPTION` reads VM lists through the installed AWS, gcloud, or Azure CLI and its existing login. This is read-only discovery. It does not create VMs or make them Tessera Nodes; an instance becomes a Node when its Tessera agent joins with the cluster token. Cloud inventory adapters are covered by scripted local tests, while real account validation remains open.

`tessera infra plan -f desired.yaml [--state current.json]` previews a file with `networks`, `machines`, and `apps`. The optional state file is a local JSON fixture with the same resource names and `owned` flags; without it, the planner treats every desired resource as missing. This preview makes no cloud calls. It orders network, machine, and App changes, produces a stable generation hash, and marks changes to networks or machines and removal of owned resources as `confirm`. Adopted resources absent from the desired file are left alone. Cloud backed state discovery and `infra apply` are still planned.

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
