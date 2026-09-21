# Roadmap

Shipped work is in [CHANGELOG.md](CHANGELOG.md). Versions below are the intended order, not dates. A release ships when its drill passes, not when the list is full.

Healing stays deterministic. A model may explain. It does not schedule, and it is not required for the cluster to come back.

## 0.1.0

Shipped. One binary, SQLite, LAN join, placement, make-before-break moves, playbook healing, snapshot promotion, read-only MCP, and Kubernetes manifest import.

## 0.2.0

Make the 0.1.0 path fast and trustworthy on real machines.

- Tested containerd runtime. `ctr` is present and untested.
- Cluster image cache, so a second start of a cached image is seconds, not a registry pull.
- Join benchmark recorded once, then on a slow cadence, and shown in `tessera get nodes`.
- Route cutover that follows a move without a client changing address.
- `tessera confirm` for proposed actions. Wipe, reimage, and delete still do not run by themselves.

## 0.3.0

Inference as a first-class placement, not a GPU count field.

- GPU inventory from the NVIDIA management library, including model and free memory.
- An App or Model with `gpus` lands only on a node that has them.
- Use the NVIDIA container toolkit. Tessera does not install drivers.
- A model server is reachable by its Route only from a fitting node.

## 0.4.0

Training jobs that survive a better machine showing up.

- Gang placement is already all-or-nothing. This release adds a shared checkpoint on NFS or object storage, not a new filesystem.
- Host or fabric network labels for workers that must share a fast link.
- A policy-allowed move checkpoints the job and restarts it on the faster gang. No live migration.

## 0.5.0

Use an existing Kubernetes cluster without migrating it first.

- Bridge: the same MCP tools and `tessera ask` against a kubeconfig. Diagnose in place.
- Convert stays the import path. Custom resources, operators, and webhooks are reported, not silently dropped.
- Point Tessera at kind or k3s, ask why a Deployment is failing, then import and run it on Tessera nodes.

## 0.6.0

The controller is cattle, including when you meant to run more than one.

- Three controller replicas, leader election, and replicated state.
- Agents reconnect. Workloads do not restart because the leader changed.
- `tessera backup` restores that store onto a new leader.

## 0.7.0

Use the cloud account you already have. AWS, GCP, and Azure are first. Others follow the same provider interface.

- A cloud VM is a Node. Placement, healing, and moves do not grow a second scheduler.
- Bring your credentials. Tessera does not become the account.
- Start or adopt machines that already have Linux and the agent, including GPU instance types.
- An existing instance can join. You do not have to recreate the account to use it.
- Capacity and score come from the instance, so a faster cloud machine can take a workload the same way a faster LAN machine does.

## 0.8.0

Describe the infrastructure you want. Tessera creates it and keeps it there.

- One file for networks, machines, and Apps. Apply it the same way as an App.
- The controller reconciles that file against the cloud account: create what is missing, leave what already matches, and show what it will not delete without confirmation.
- A change to the file is a generation, with the same rollback rule as an App.
- This is Tessera's own intent, not a Terraform clone. Import from an existing cloud layout can come after apply works.

## Later

Not scheduled. A machine that already has Linux and the agent does not need this.

- Netboot a prebuilt image that already contains the agent. Time bound is boot time, not a package install.
- This is not a general OS installer.
