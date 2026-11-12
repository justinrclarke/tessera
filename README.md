# Tessera

Tessera is a single Go binary that runs containers across machines on a LAN. You say what should be running. The controller places it, restarts it, and moves it. You do not assemble Deployments, Services, and probes by hand.

Healing is deterministic. A model is optional, and only for explanation. If a standalone controller dies, a node with the newest signed snapshot can take over. With three controller replicas, a majority elects the replacement and agents reconnect without restarting running work. Wipe, reimage, and delete are never done automatically.

What is coming next is in [ROADMAP.md](ROADMAP.md).

## Run

Published 0.4.0 binaries are available for macOS and Linux, amd64 and arm64, from the [latest release](https://github.com/justinrclarke/tessera/releases/latest), with SHA-256 checksums. Download the archive for your platform, extract it, and run `./tessera version`. To build from source instead:

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

If an App, Model, or Job has no placement, ask `tessera ask "why is web down?"`. Placement findings explain absent or unavailable nodes, CPU and memory reservations, host-port conflicts, node-label mismatches, and GPU count, model, memory, or device reservations. Gang Jobs report when the remaining workers cannot fit together on the required fabric. A blocked App update explains the spare capacity needed to keep the previous generation running. Missing Config or Secret references are reported for unresolved environments; saved release environments remain usable for rollback. These findings also appear through the read-only MCP `diagnose` and `explain` tools. They do not propose or execute actions. A newly submitted workload that fits may simply be waiting for its agent; a dependency that is not running is reported separately.

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

Run one Tessera agent per Docker daemon. Docker workloads carry their node owner, and agents retain only their own containers or recorded legacy assignments. Agent startup locks the Docker daemon identity until exit. Agents in separate containers must share a lock filesystem with `agent --lock-dir /shared/tessera-locks`; a filesystem lock cannot fence separate hosts using a remote daemon. Keep one agent per containerd namespace. The Docker lab gives each node its own daemon. Use disposable stateless workloads for the trial; general persistent volumes and stateful identity are not implemented.

Kinds: App, Job, Model, Route, Config, Secret, Policy. Separate documents with `---`.

A Job runs to completion. Set `gang: true` when every replica must land together or not at all. A Model is an App that prefers GPU nodes. A Route listens on a fixed controller port and balances new TCP connections across ready replicas. If a backend cannot be reached, it tries another before forwarding any bytes. Updates and moves become eligible for routing only after readiness. Use the independent gateway below when the client address must survive loss of a controller.

Start an agent with `--labels fabric=ethernet-a,storage=shared` to describe its host. An App or Job can require `node_labels: {storage: shared}`. For a gang Job, `gang_fabric: fabric` keeps all workers on nodes with the same `fabric` value. Labels describe a real link or shared mount; Tessera does not create either one.

`cpu` accepts Kubernetes-style quantities (`100m`, `1`). `memory` accepts `128Mi`, `1Gi`, or a byte count.

For an NVIDIA model server, set `kind: Model`, `gpus: 1`, and optionally `gpu_model: NVIDIA H100` and `gpu_memory: 20Gi`. The agent reads model and free memory through `nvidia-smi`; Tessera reserves a GPU UUID for each replica and asks Docker to expose that device. The host needs an NVIDIA driver and the NVIDIA Container Toolkit. GPU memory is checked at placement time and is not a container memory limit. The `ctr` runtime currently rejects GPU workloads. Attach a Route to the Model name as you would for an App.

A release change (image, command, environment, ports, resources, health, placement constraints, or dependencies) bumps generation. Referenced Config and Secret data changes also create a generation; Tessera saves the resolved environment with that release. Replica-only changes do not. Rollback restores the previous workload's values without changing the shared Config or Secret object. A rejected multi-document apply leaves existing resources unchanged.

For Apps, Tessera starts one replacement at a time and keeps the old assignment until the replacement is ready. A generation becomes healthy only when all desired replicas are ready. A failed generation rolls back when a prior healthy generation exists. Spare resources are required; Tessera holds the old workload when no replacement fits. Fixed host ports require a different available node. Jobs retain their cancel-and-replace behavior on a generation change.

`health` checks an HTTP path on any published container port. Routes select the matching host mapping using `target_port`. Only 2xx responses pass; redirects do not. `timeout` defaults to `2s` per request and `startup_timeout` to `60s`. Until readiness passes, the assignment is `starting` and Routes exclude it. Startup timeout stops the failed replacement and allows rollback. Later readiness failure removes that replica from routing while leaving its process running and checking for recovery; this is not a liveness restart policy. Probe timing survives agent restart. The verified rollout path uses Docker; containerd host-network port placement still needs acceptance. Set `drain_timeout: 30s` on an App to retain existing TCP connections to its retired backend for up to that interval. New connections use its ready replacement. The old container remains running through the drain interval plus a two-second gateway refresh allowance. Without this setting, cutover can close old connections. Route deletion or credential revocation closes them immediately. A simultaneous long control-plane outage can outlast the refresh allowance.

## Stable service access

Run the gateway on a host that stays available independently of the controllers, using the cluster token and all three controller addresses:

```sh
export TESSERA_URL=http://controller-a:7468,http://controller-b:7468,http://controller-c:7468
export TESSERA_TOKEN=your-cluster-token
./tessera gateway --route web --port 8080
```

Clients use `http://gateway-host:8080/`. The gateway refreshes ready backend addresses once per second and connects directly to the nodes. It keeps its last accepted backend set during controller outages, so an election or loss of quorum does not stop existing traffic. An authoritative missing Route or authorization failure clears that set. It rejects older controller epochs. A first start requires a reachable controller and an existing Route; a restart cannot recover cached routing while all controllers are unavailable.

The listen port binds on all interfaces. On the same host as a controller, choose a different port, such as `--port 18080`, to avoid conflicting with its Route listener. The gateway requires network access to published node ports. It balances TCP connections, does not replay application requests, and may close connections when their backend is removed. Run it in a separate terminal, or use `tessera install --role gateway --route web --port 18080`. Automatic failover of the gateway itself is not implemented. High availability of that address still needs an external load balancer or floating-address arrangement. Controller loss and gateway-host loss are separate acceptance cases.

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

Set `push: false` to keep the image local. The pushed App image is pinned to the digest reported by the registry. Changes to the base tag or package repository between builds can produce different image IDs; pin the base image and package versions when repeatability matters. Each built release records its base, package inputs, command, source-tree digest, and resulting image reference. Changing the source while Docker builds is rejected. The source digest includes the whole context, including files excluded by Docker, and is provenance rather than an image signature.

`tessera build -f app.yaml -o release.yaml` builds without changing a cluster. Add `--archive images.tar` with `build.push: false` to produce a private Docker image package. Transfer both files, run `docker load -i images.tar` on the destination runtime, then apply `release.yaml`. Archives keep the exact local content tag; pushed digest releases use their registry instead. No deb/rpm packaging or registry credential distribution is provided.

## Checkpointed gang Jobs

A gang Job can set `checkpoint: {directory: /mnt/training, timeout: 2m}`. Mount the same NFS or other shared filesystem at that absolute path on every eligible node, then select those nodes with labels. Tessera bind-mounts that directory into the workers. It does not create the mount or install storage software. Object-store checkpoint adapters are not implemented.

When a policy permits a faster-gang move, Tessera reserves every replacement and writes `requests/ASSIGNMENT_ID.json` inside the shared directory. The worker must watch its request, save and sync its own data under the request URI's `ASSIGNMENT_ID` subdirectory, then run:

```sh
tessera checkpoint commit --request /mnt/training/requests/ASSIGNMENT_ID.json --assignment ASSIGNMENT_ID --progress 100
```

The helper validates the request and atomically writes its completion marker; the training program is responsible for saving and syncing usable data first. The controller waits for every committed marker before requesting any stop, then waits for every old agent's stop acknowledgement before starting replacements. Requests that expire before this point leave the original gang running and release reservations. Requests and move phases survive snapshots and database recovery.

Workers receive `TESSERA_ASSIGNMENT_ID`, `TESSERA_WORKER_RANK`, and `TESSERA_CHECKPOINT_DIRECTORY`. Resumed workers additionally receive `TESSERA_CHECKPOINT_URI`, `TESSERA_CHECKPOINT_GENERATION`, `TESSERA_CHECKPOINT_SOURCE_ASSIGNMENT`, and `TESSERA_CHECKPOINT_PROGRESS`. The program must load that checkpoint and resume. Once the old gang has stopped, an unavailable replacement waits for agent recovery or operator intervention; Tessera does not presume an unreachable worker has stopped. This contract is locally tested with two simulated workers and a shared directory. Actual NFS behavior and training frameworks still need machine acceptance.

## What it does on its own

The agent heartbeats. After 3 seconds a node is suspect. After 10 seconds it is dead and its work is placed elsewhere. Crash loops restart up to 3 times, then move. A bad release rolls back. A full disk is pruned. A certificate past half its life is renewed. A node that keeps failing workloads is cordoned, then uncordoned after the move cooldown if heartbeats are fresh.

If a faster node appears and the gain is at least 15 percent, a stateless app is started there first. The old copy stops only after the new one is running. Moves wait out a 10 minute cooldown.

If the leader's lease expires, a node holding the snapshot can promote itself. A node that is alone waits 30 seconds, so a short partition does not create a second writer. Agents ignore assignments from an older epoch.

`tessera delete app NAME` or `tessera delete node ID` proposes removal and prints the confirmation ID. `wipe`, `reimage`, and `delete` are recorded as proposed. Heal does not run them, even if you put them in `auto`. `tessera confirm` lists proposed actions. `tessera confirm <id>` is the only way one runs. Delete removes that app or node. Wipe stops Tessera containers on the node, prunes its runtime, and clears the agent data directory, then removes the node. Reimage does that clear and the agent rejoins empty. Neither wipes the operating system.

## Ask, and MCP

`tessera ask` answers from findings and the action log. If `TESSERA_LLM_URL` and `TESSERA_LLM_KEY` are set, a short note is appended. The model is not required, and it is not on the heal path.

`tessera mcp` speaks MCP over stdio. Tools are read-only: `list_apps`, `get_app`, `list_nodes`, `get_node`, `logs`, `diagnose`, `list_actions`, `explain`, `metrics`.

## Scoped credentials

The original cluster token is an administrator credential and is also the immutable replication and snapshot-signing root. Trusted legacy agents can retain full snapshots and promote a standalone controller. For new workers, mint a short-lived, node-bound bootstrap token on the administrator's machine:

```sh
tessera token join --node worker-a --ttl 10m
```

Transfer the printed token privately. Start that node with `TESSERA_TOKEN=JOIN_TOKEN tessera agent --restricted --id worker-a --url http://controller:7468`, or add the same flags to `install`. Registration saves a private scoped node credential. Restarts use it after the bootstrap token expires. Workers can access their own assignments and image downloads, and report their own status. They cannot read other nodes' assignments, cluster Secrets or snapshots, or change manifests and confirmations. Restricted workers do not promote standalone controllers or upload images to the controller cache; supply a reachable registry or operator-populated cache for them.

`tessera token gateway --route web` prints a credential limited to that Route's backends and controller status. Use it as the gateway's `TESSERA_TOKEN`. `tessera token revoke --node worker-a` revokes the node credential and all previously minted bootstrap tokens for that node. Mint a fresh bootstrap token to authorize rejoining.

`tessera token rotate` changes the administrator API credential while retaining scoped workers and gateways. First rejoin all legacy nodes as restricted workers; rotation refuses while any trusted legacy node remains. The CLI verifies the new key and updates private `client.json`, preserving the replication root in `token`. Controller restart preserves the saved API credential, and an existing store refuses a different replication root. Update any `TESSERA_TOKEN` overrides. An ambiguous network result keeps a private `pending-api-token`; `tessera token recover` verifies that key before updating the client. Keep that file until recovery succeeds. API traffic still requires a trusted LAN or an HTTPS reverse proxy; scoped credentials do not encrypt HTTP.

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

Install controller and gateway services into separate data directories:

```sh
tessera install --role controller --data ~/.tessera-controller --advertise-url http://controller:7468 --token "$TESSERA_TOKEN"
tessera install --role gateway --data ~/.tessera-gateway --url http://controller:7468 --route web --port 18080 --token "$TESSERA_TOKEN"
```

Controller replica installation additionally accepts `--id`, `--raft-listen`, `--peers`, and the first-start `--bootstrap` flag. Clear `--bootstrap` after initialization. Services and logs have distinct role names. Generated files and service commands are tested for macOS and Linux; reboot, native service-manager upgrades, mixed-version rolling upgrades, and schema downgrade compatibility remain unverified. Retain a database backup and its matching binary before upgrading; restore into a new directory for rollback.

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

The opt-in three-host drill uses SSH to run this checkout on three disposable Linux machines. It builds the required amd64 or arm64 binaries locally; the hosts do not need Go or an installed Tessera binary. The first host needs access to its system Docker socket and a cached `busybox:latest` image. It must have no existing Tessera agents or containers. SSH must work without an interactive prompt. Each `address` is reachable from the other hosts and the computer running the test; agents must advertise a reachable IPv4 address. Allow TCP ports 17468–17470 between the machines and access to the worker's dynamically published Docker ports from the test computer and controllers. `TESSERA_ACCEPT_PORT` changes the starting port.

```sh
export TESSERA_ACCEPT_HOSTS='[{"ssh":"user@host-a","address":"10.0.0.11"},{"ssh":"user@host-b","address":"10.0.0.12"},{"ssh":"user@host-c","address":"10.0.0.13"}]'
go test ./cmd/tessera -run '^TestThreeHostControllerAcceptance$' -count=1 -timeout 10m -v
unset TESSERA_ACCEPT_HOSTS
```

The drill checks distinct Linux machine IDs, starts three controllers and one Docker agent, then kills controller processes while continuously requesting one gateway address on the test computer. It measures leader election, replica catch-up, quorum recovery, full cluster restart, and offline restore. A minority must reject a write with HTTP 503; the rejected App update must remain absent after recovery. Assignment ID, Docker container ID, start time, and restart count must stay unchanged. The offline replacement starts only after all three original controllers have stopped. This tests controller process loss across hosts, not host power loss, node rescheduling, gateway-host failover, or connection draining.

Private logs and a JSON report are saved under `lab/results/controllers-*`; set `TESSERA_ACCEPT_RESULTS` to choose another directory. Cleanup removes the drill's processes, workload containers, and temporary stores, leaving the cached image in place. An incomplete process cleanup preserves its temporary store and the worker's `/tmp/tessera-accept-docker.lock` for inspection. Run Docker drills sequentially; the acceptance harness reserves its test runtime and fixed ports.

Rehearse the same sequence locally with `TESSERA_ACCEPT_LOCAL=1 go test ./cmd/tessera -run '^TestThreeHostControllerAcceptance$' -count=1 -timeout 10m -v`. This mode uses three local controller processes and real Docker, and records `local-rehearsal` in its report. It does not satisfy the three-machine gate. The normal test suite skips this drill unless one of these environment settings is supplied.

Databases, sidecars, and backups contain cluster credentials and Secret values and use private file permissions. Raft snapshot directories are private. Keep application-data backups separate: `tessera backup` copies control-plane state and does not back up volumes or container filesystems. See the trial and reliability gates in [ROADMAP.md](ROADMAP.md) for remaining installation, upgrade, gateway availability, access-control, and capacity gates. The default drill verifies HTTP service through slow startup and invalid-image rollback. With a cached `busybox:latest`, `TESSERA_DOCKER_SMOKE=1 go test ./internal/drill -run TestDockerReadinessRolloutSmoke -count=1 -v` verifies actual Docker startup, ready cutover, and failed-command rollback using only containers created by that test.

`tessera up` restarts itself through a watchdog unless you pass `--watched` or set `TESSERA_WATCHED=1`. A clean exit is not restarted. A child that dies within 500ms is not restarted either.

## Cloud infrastructure

`tessera cloud inventory --provider aws --region us-east-1`, `--provider gcp --project PROJECT`, or `--provider azure --subscription SUBSCRIPTION` reads VM lists through the installed AWS, gcloud, or Azure CLI and its existing login. Provisioning uses the same credentials on the elected controller. Provider account credentials stay on the controller; workers receive only their own expiring join credential.

```yaml
providers:
  aws:
    ssh_key: operator-key
networks:
- name: private
  provider: aws
  region: us-east-1
  cidr: 10.42.0.0/24
machines:
- name: worker
  provider: aws
  region: us-east-1
  type: t3.small
  image: ami-your-prebuilt-linux-docker-tessera
  network: private
apps:
- name: web
  image: registry.example.com/team/web@sha256:YOUR_DIGEST
  replicas: 1
```

```sh
tessera infra plan -f infrastructure.yaml
tessera infra apply -f infrastructure.yaml
tessera infra status
tessera infra rollback
```

Planning reads recorded controller state when configured, or `--state current.json` for a local preview; it makes no cloud calls. Apply records intent, ownership keys, provider IDs, and a stable generation before external effects. It creates networks before machines and waits for their agents before applying Apps. AWS networks use a recoverable CloudFormation stack; GCP uses a custom network and subnet; Azure uses a virtual network and subnet. Reconciliation retries interrupted operations by ownership key, periodically inspects owned resources, and recreates missing machines with a fresh provider idempotency key. The local tests cover lost responses, snapshot recovery, repeated apply, replacement and rollback keys, adoption, confirmation, and abandonment.

Images must already contain Linux, systemd, Docker, and `tessera` on PATH. Bootstrap installs a system service with an expiring node-bound join token and private scoped worker credentials. Configure a reachable controller address and networking before trying this: private networks do not include NAT, internet gateways, peering, VPN setup, or firewall rules. GCP provisioning disables an external IP; Azure disables public IP and automatic NSG creation. Azure requires `resource_group`, `admin_user`, and `ssh_key` provider settings; GCP uses `project`. For GCP, machine and network `region` values use the same zone; Tessera derives the subnet region. Provider settings cannot change while recorded machines or networks exist.

Set `adopt: PROVIDER_INSTANCE_ID` and `node_id: EXISTING_NODE_ID` on a machine to record an existing instance. Join its agent separately. Adopted machines are never removed or replaced. An App that already exists outside the intent is also retained when removed from that file. Every owned removal or replacement proposes a `delete` action; inspect it with `tessera confirm` and explicitly confirm its ID. A machine with active workloads and a network with attached recorded machines remain blocked. Drain workloads before changing their host. VM removal does not promise cleanup of every provider-created disk or interface.

`infra rollback [--generation HASH]` selects a prior intent; it uses the same reconciliation and confirmation rules. It does not revert application data. Resolve a pending provider operation before changing intent. If its result cannot be determined, `tessera infra abandon-pending` proposes an abandonment requiring confirmation; it preserves the original intent and ownership key for manual resource review, then allows rollback. It does not delete possible orphaned resources.

AWS, GCP, and Azure adapters are covered by scripted CLI responses. No cloud account was accessed for these tests; disposable live-account provisioning, joining, connectivity, and cleanup acceptance remain open. Provider CLI references: [AWS](https://docs.aws.amazon.com/cli/latest/reference/ec2/run-instances.html), [GCP](https://docs.cloud.google.com/sdk/gcloud/reference/compute/instances/create), [Azure](https://learn.microsoft.com/en-us/cli/azure/vm?view=azure-cli-latest).

## Check

```
go test ./...
go test ./internal/drill -count=1
```

`tessera get nodes` shows the join benchmark: CPU, memory, and disk. It is recorded once, then refreshed hourly.

`tessera drill` runs the same acceptance suite: placement, dead-node reschedule, rollback, a refused wipe, epoch fencing, restore from the local cache, leader promotion, a move onto a faster node, certificate renewal, route cutover, the image cache, and confirm. It also verifies a two-worker checkpoint move and scoped-worker continuity through API token rotation. It does not need Docker.

History retention keeps three signed snapshots, the latest 1,000 completed actions, and 1,000 eligible terminal assignments. Proposed and confirmed actions, active assignments, completed workers from the current Job generation, and assignments referenced by active checkpoint moves are preserved. Active App history retains its latest 20 releases plus the healthy rollback release. History pruning runs every 128 snapshots. Image-cache files need operator disk management; these bounds do not establish cluster-size or disk-exhaustion guarantees.

Optional local checks, run sequentially against a disposable Docker environment:

```sh
TESSERA_DOCKER_REGISTRY_SMOKE=1 go test ./internal/drill -run TestPublishedBuildSecondClusterAndDigestRollback -count=1
TESSERA_DOCKER_BUILD_SMOKE=1 go test ./cmd/tessera -run TestBuildOfflineArchiveRestoresManifestReference -count=1
```

The registry drill runs two independent temporary Tessera controllers and real Docker agents, removes local image references before the second cluster pulls, and verifies failed-release rollback preserves the previous digest and build record. Both clusters share the local Docker daemon with distinct workload ownership; this proves the local registry path, not cross-machine access or registry authentication distribution.

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
