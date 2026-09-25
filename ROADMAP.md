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

`install` only writes a unit file. `ask` is one question.

## 0.1.1

Shipped as the priority roadmap. The CLI below is the next build, not this tag.

The CLI is how you install Tessera, command it, and talk to the agent. One binary. Not a second tool. Nothing else matters if a person cannot install it and ask it what is wrong.

- `tessera install` installs the binary and the user service. You do not write a unit file by hand.
- Commands stay here: apply, get, import, backup. The node process stays `tessera agent`, started by the service or by hand.
- A session talks to the agent. A line that is a command runs. Any other line is a question. Exit leaves the cluster running.

## 0.2.0

The 0.2.0 implementation and local acceptance path are complete on macOS with OrbStack. Runtime work includes the tested containerd runtime, cluster image cache, join benchmark on an hourly cadence, route cutover, and `tessera confirm`. The Docker lab has passed its clean-state release drill on the available local engine.

Wipe, reimage, and delete run only after `tessera confirm`. Heal does not run them, even if they are listed in `auto`. Wipe and reimage clear Tessera state on that node. They do not wipe the operating system. Netboot stays unscheduled.

The Docker lab lets someone evaluate Tessera on one machine before using real nodes or cloud accounts. It is a repeatable test environment, not a claim that Docker reproduces a cloud provider.

The lab is in the tree: Compose starts two isolated Docker daemons and agents; a sample app uses a Tessera Route; k6 has five load profiles; scripts inject process, node, latency, HTTP error, and confirmed-delete scenarios; optional registry, PostgreSQL, Redis, and S3-compatible services are available. A small DNS relay lets Tessera workloads use stable Compose service names from both isolated nodes. The sample app checks every optional service through those names. Only the first node receives the sample image initially; the clean-state drill confirms the second node obtains it through Tessera's leader image cache during failover. `lab/lab.sh verify` passed on macOS with OrbStack and a Linux Docker engine. It saves k6 results and removes lab containers and volumes when done.

- One documented start command brings up a controller and at least two isolated Docker-backed nodes. A sample app is placed through Tessera and reached through a Route. Users can replace the sample image and manifest with their own services.
- Built-in k6 smoke, baseline, spike, soak, and failure runs report latency and error rate with explicit pass criteria. Node-loss runs time Route recovery. Runs use local endpoints and save results for comparison.
- Repeatable scenarios cover process crashes, node loss and recovery, injected latency and HTTP errors, route continuity, and confirmed destructive actions. The suite shows assignments and actions so a user can see why behavior changed.
- Optional local dependencies represent common cloud building blocks: a registry, PostgreSQL, Redis, and S3-compatible object storage. They require no cloud credentials and are opt-in so the basic lab stays small.
- The release drill starts the lab from a clean state, verifies the sample service and failover, runs a short k6 check, and tears down its own containers and volumes. The README documents Docker Desktop and Linux requirements and the differences from real cloud infrastructure.

Docker Desktop and native Linux host compatibility are unverified. Before advertising support for either platform, run `go test ./... -count=1` and `sh lab/lab.sh verify` there. Both runs must exit successfully, show all optional service checks as true before and after failover, and leave no lab containers. The verified local path uses OrbStack's Linux Docker engine; its nested node daemons, service-name DNS, and image-cache transfer passed. Keep provider API emulation out of the default lab until a specific provider workflow is needed.

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
