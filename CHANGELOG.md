# Changelog

## 0.4.0 (2026-10-06)

Controller reliability, HTTP readiness, safe App updates, and an independent service gateway. Verified locally with three controller processes and Docker on OrbStack. Cross-machine acceptance, connection draining, gateway-host failover, and live service-manager upgrades remain open.

- Apps support HTTP readiness and bounded startup, with probe state preserved across agent restart. Routes exclude unready replicas; later readiness failure removes a replica from routing without restarting its process.
- App updates replace one assignment at a time and retain the previous service until its replacement is ready. Ports, resources, health, dependencies, and referenced Config/Secret data participate in release changes. Resolved environment values survive rollback; all desired replicas must become ready before a generation is marked healthy. Failed apply is atomic, and failed generations cannot reuse release numbers or suppress later rollback.
- Routes balance TCP connections across ready backends and try another when connection establishment fails. `tessera gateway --route NAME --port PORT` provides a client address independent of controller processes and retains accepted backends during controller outages. Missing Routes or rejected credentials clear its routing; gateway-host failover and connection draining remain open.
- Added HTTP traffic acceptance for slow startup, invalid-image rollback, and abrupt three-controller leader/quorum loss at one gateway address. A disposable Docker rollout test passed on OrbStack and preserved the healthy container through a failed-command update.
- Added `tessera get controllers` to inspect each controller's role, fencing epoch, committed and applied log indexes, and ability to accept writes. Followers and minority controllers remain inspectable without redirecting; the endpoint requires the cluster token.
- Replica clients retry truncated response bodies with the original mutation ID. The acceptance drill now loses the response body during a leader election. Image transfers retain the caller's deadline instead of inheriting the short controller request timeout.
- Added a real three-process TCP/TLS acceptance test that abruptly kills a controller, loses quorum, restarts a replica from its existing store, and verifies running workload identity and zero container restarts. It runs in `go test ./...` without Docker. Physical-machine acceptance remains open.
- Registration preserves a node's cordon until the cooldown and fresh-heartbeat conditions allow uncordoning. Late status reports cannot revive stopped or succeeded assignments. Multiple replicas can share a node, completed moves do not cause extra replicas and can move again after cooldown, and partial Job completion counts toward the desired worker count.
- Docker inspects exited Tessera containers for their actual exit code and OOM state, so successful Jobs are not mistaken for crashes. A process that exits immediately after start cannot mark an App release healthy. Disposable Docker container and successful Job smoke tests passed on the local OrbStack engine.
- Agent fencing state and client configuration use atomic, durable private-file replacement. A corrupt agent cache stops startup without replacing the saved identity. SQLite databases, sidecars, and backups have private permissions; Raft snapshot directories are private.
- Replicated retry receipts are pruned through consensus: operator mutations are retained for 24 hours, and registration, heartbeat, and status receipts for two minutes. Legacy receipts remain available. Receipt retention is an automatic retry window, not permanent deduplication of manually reused IDs.
- App, Route, and Policy manifests reject unknown top-level fields. Apps reject invalid readiness probes, TCP ports, and negative resource or replica requests. Explicit `replicas: 0` scales an App down. Kubernetes import reports workloads requiring storage, sidecars, init containers, probes, security context, or environment references as skipped instead of silently deploying a partial conversion.

## 0.3.0 (2026-10-06)

Kubernetes bridge and CLI completion, with early implementations of later roadmap milestones included below. This release includes the 0.2.0 runtime and Docker lab changes.

- Added read-only Kubernetes inspection to `tessera ask` and the existing MCP tools through `kubectl`, with kubeconfig, context, and namespace selection.
- Cluster import now includes StatefulSets and DaemonSets and reports unsupported built-in kinds, custom resource definitions, and webhook configurations. Multiple Service ports convert to distinct Routes.
- Added a disposable kind-to-Tessera drill. It diagnosed an image pull failure, imported a Deployment and Service, and verified the App through a Tessera Route on macOS with OrbStack. Custom resource instances and operator behavior are not converted; broader RBAC and large-cluster validation remain open.

- `tessera install` now installs the binary and starts a user service running `tessera agent` on macOS or Linux. It accepts controller, runtime, label, and directory settings, keeps tokens in private files, replaces the binary atomically on upgrade, and offers `--no-start` to stage files. Live service-manager acceptance on both platforms remains open.
- `tessera` and `tessera session` open a session for CLI commands and questions. Quoted paths work, command errors leave the session open, and exiting leaves the cluster running. A controller-backed CLI test covers apply, get, and ask; service-manager commands use scripted tests.
- Agents use a saved controller address even when the token came from the token file, and persist an address discovered for an installed node. CLI environment overrides also work before a client file exists.

### Included early work

- Controller replication (0.4.0 roadmap): added three-controller consensus with Hashicorp Raft, an atomic replicated command log through the existing SQLite connection, and mutual TLS for replication. Followers redirect clients, and minority partitions cannot commit resource changes.
- Agents and CLI clients learn all three addresses and retry through elections. Successful mutation receipts survive leader changes, running assignment IDs stay fixed, and agents persist fencing epochs and decline standalone promotion in replica clusters.
- The acceptance drill covers a lost update response, leader failover, workload continuity, restarted replica catch-up, a partitioned old leader, and offline recovery. Real local TCP/TLS tests cover full restart from a snapshot and subsequent log entries. Multi-machine acceptance remains open; membership is fixed and new clusters require empty stores.
- Offline `tessera restore` preserves committed resources and release history from replica backups while clearing old consensus state and advancing the epoch. `tessera backup` reads a live database without opening another writer. Image cache files remain separate.
- GPU placement (0.5.0 roadmap): agents report NVIDIA GPU UUIDs, model names, and free memory from `nvidia-smi`. Apps and Models can request a GPU model and minimum free memory; placement reserves distinct devices and Docker passes their UUIDs to the NVIDIA Container Toolkit. The containerd runtime reports GPU workloads as unsupported. NVIDIA host acceptance remains open.
- Training topology (0.6.0 roadmap): agents accept host labels; Apps and Jobs can select them. Gang Jobs can require all workers to share one fabric label value. Checkpoint and coordinated gang moves remain open.
- Image builds (0.7.0 roadmap): an App build stanza creates an OCI image during `tessera apply`. It can install `apk` or `apt` packages, copy a source context, and set the process. Local images use an image-ID tag; published images use a registry digest. A disposable local registry push/pull drill passed on OrbStack. Package artifacts and cross-cluster acceptance remain open.
- Cloud inventory (0.8.0 roadmap): added read-only VM inventory through the locally installed AWS, gcloud, and Azure CLIs, with scripted tests for all three providers. Provisioning and live account validation remain open.
- Infrastructure planning (0.9.0 roadmap): added a pure planner for networks, machines, and Apps. It shows stable generations, orders create actions by dependency, and proposes destructive changes for confirmation. The CLI currently compares with a local state file; no cloud apply path exists yet.

## 0.2.0 (2026-10-06)

- A local Docker lab starts two isolated nodes and a sample Route, offers k6 load profiles and failure scenarios, and provides optional registry, PostgreSQL, Redis, and S3-compatible services. The sample workload reaches those services by name from either node. The clean-state drill verifies image transfer through the leader cache on failover and passed on macOS with OrbStack. Docker Desktop and native Linux host compatibility remains unverified.
- `ctr` is tested. It uses the `tessera` namespace, skips a pull when the image is already present, and reports a host port.
- A cluster image cache on the leader. A second start of a cached image does not pull from the registry.
- Join benchmark is recorded once, refreshed hourly, and shown by `tessera get nodes`.
- A route keeps its address and follows a move once the replacement is running.
- `tessera confirm` runs a proposed wipe, reimage, or delete. Heal still does not.

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
