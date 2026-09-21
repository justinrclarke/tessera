# Roadmap

Shipped work is in [CHANGELOG.md](CHANGELOG.md). Version order is priority order, most needed now first. Not dates. A release ships when its drill passes, not when the list is full.

Healing stays deterministic. A model may explain. It does not schedule, and it is not required for the cluster to come back.

Dependencies stay on the known set. Do not add an unknown module to get a feature done. The standard library is fine. Direct modules today, and the trees they already pull, are the allowed set:

- `gopkg.in/yaml.v3`
- `github.com/hashicorp/mdns`
- `github.com/modelcontextprotocol/go-sdk`
- `golang.org/x/sys`
- `modernc.org/sqlite`

A new direct module is a roadmap change, not a pull request convenience.

## 0.1.0

Shipped. One binary, SQLite, LAN join, placement, make-before-break moves, playbook healing, snapshot promotion, read-only MCP, and Kubernetes manifest import.

`install` only writes a unit file. `ask` is one question. `ctr` is present and untested. A cold image pull is not seconds. A Route does not yet follow a move.

## 0.1.1

Shipped as the priority roadmap. The CLI below is the next build, not this tag.

The CLI is how you install Tessera, command it, and talk to the agent. One binary. Not a second tool. Nothing else matters if a person cannot install it and ask it what is wrong.

- `tessera install` installs the binary and the user service. You do not write a unit file by hand.
- Commands stay here: apply, get, import, backup. The node process stays `tessera agent`, started by the service or by hand.
- A session talks to the agent. A line that is a command runs. Any other line is a question. Exit leaves the cluster running.

## 0.2.0

Make the shipped path true on a real machine. The claims in 0.1.0 fail without this.

- Tested containerd runtime. `ctr` is present and untested.
- Cluster image cache, so a second start of a cached image is seconds, not a registry pull.
- Join benchmark recorded once, then on a slow cadence, and shown in `tessera get nodes`.
- Route cutover that follows a move without a client changing address.
- `tessera confirm` for proposed actions. Wipe, reimage, and delete still do not run by themselves.

## 0.3.0

Use an existing Kubernetes cluster without migrating it first. Import already converts files. It does not operate the cluster people already have.

- Bridge: the same MCP tools and `tessera ask` against a kubeconfig. Diagnose in place.
- Convert stays the import path. Custom resources, operators, and webhooks are reported, not silently dropped.
- Point Tessera at kind or k3s, ask why a Deployment is failing, then import and run it on Tessera nodes.

## 0.4.0

The controller is cattle, including when you meant to run more than one. Snapshot promotion already covers a dead leader. This is the step after that, not before the daily path works.

- Three controller replicas, leader election, and replicated state.
- Agents reconnect. Workloads do not restart because the leader changed.
- `tessera backup` restores that store onto a new leader.

## 0.5.0

Inference as a first-class placement, not a GPU count field. Needs a node path that already places and routes.

- GPU inventory from the NVIDIA management library, including model and free memory.
- An App or Model with `gpus` lands only on a node that has them.
- Use the NVIDIA container toolkit. Tessera does not install drivers.
- A model server is reachable by its Route only from a fitting node.

## 0.6.0

Training jobs that survive a better machine showing up. Gang placement is already all-or-nothing. Checkpoint is not.

- A shared checkpoint on NFS or object storage, not a new filesystem.
- Host or fabric network labels for workers that must share a fast link.
- A policy-allowed move checkpoints the job and restarts it on the faster gang. No live migration.

## 0.7.0

Build an image or package for a specific stack, and publish it so other people can pull it. Depends on the 0.2.0 cache. That cache is not the public registry.

- A stack file names the base, the packages, and the process that should be running. Apply it the same way as an App.
- Tessera builds it. The result is an OCI image or a package, not a container that exists only on one node.
- Publish to a public registry the author names. Another cluster pulls that name. There is no private copy step.
- A published name is immutable for that generation. A new build is a new generation, with the same rollback rule as an App.

## 0.8.0

Use the cloud account you already have. AWS, GCP, and Azure are first. Others follow the same provider interface. A cloud VM is a Node. Do not build this before a LAN node is boring.

- Placement, healing, and moves do not grow a second scheduler.
- Bring your credentials. Tessera does not become the account.
- Start or adopt machines that already have Linux and the agent, including GPU instance types.
- An existing instance can join. You do not have to recreate the account to use it.
- Capacity and score come from the instance, so a faster cloud machine can take a workload the same way a faster LAN machine does.

## 0.9.0

Describe the infrastructure you want. Tessera creates it and keeps it there. This waits on 0.8.0. There is no cloud to reconcile before that.

- One file for networks, machines, and Apps. Apply it the same way as an App.
- The controller reconciles that file against the cloud account: create what is missing, leave what already matches, and show what it will not delete without confirmation.
- A change to the file is a generation, with the same rollback rule as an App.
- This is Tessera's own intent, not a Terraform clone. Import from an existing cloud layout can come after apply works.

## Later

Not scheduled. A machine that already has Linux and the agent does not need this.

- Netboot a prebuilt image that already contains the agent. Time bound is boot time, not a package install.
- This is not a general OS installer.
