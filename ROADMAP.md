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

The read-only bridge is implemented. `ask` and the existing MCP tool names can inspect a selected kubeconfig, context, and namespace through `kubectl`. Import converts the supported built-in kinds, inventories unsupported kinds and custom resource definitions and webhooks, and reports anything it cannot inspect. The isolated `lab/kubernetes.sh` drill passed on kind: Tessera explained a failed image pull, imported the Deployment and Service, and served the App through a Tessera Route. The script leaves the user's current context and existing kind clusters untouched.

- Bridge: the same MCP tools and `tessera ask` against a kubeconfig. Diagnose in place.
- Convert stays the import path. Custom resources, operators, and webhooks are reported, not silently dropped.
- Point Tessera at kind or k3s, ask why a Deployment is failing, then import and run it on Tessera nodes.

Custom resource instances and operator behavior remain outside the conversion boundary; their definitions are reported so users can plan manual migration. The bridge needs read access to workloads, pods, nodes, and events. Broader RBAC and large-cluster performance still need validation before claiming general Kubernetes compatibility.

## 0.4.0

The controller is cattle, including when you meant to run more than one. Snapshot promotion already covers a dead leader. This is the step after that, not before the daily path works.

The offline store recovery path is implemented: `tessera restore` validates a `tessera backup` database, refuses to overwrite an existing store, retains the cluster token, and starts a replacement identity at a higher epoch. Three-controller consensus and live state replication are still open; the single-writer and fencing rules remain release requirements.

- Three controller replicas, leader election, and replicated state.
- Agents reconnect. Workloads do not restart because the leader changed.
- `tessera backup` restores that store onto a new leader.

Implementation gate: choose an established consensus implementation as an explicit direct-dependency roadmap change, then route every controller mutation through one replicated command log. A follower must reject writes and direct clients to the elected leader. A majority must be required to commit; minority partitions must stop writes, and epochs must fence stale assignment commands. The acceptance drill starts three controllers, kills the leader during an App update, verifies one committed generation and uninterrupted workload, then partitions the old leader and proves it cannot mutate state. SQLite remains a local state machine store, never a concurrently shared writer.

## 0.5.0

Inference as a first-class placement, not a GPU count field. Needs a node path that already places and routes.

The initial GPU path is implemented: agents query NVIDIA's management interface through `nvidia-smi`, report model and free memory, and the scheduler reserves specific GPU UUIDs for each placement. App and Model manifests can set `gpus`, `gpu_model`, and `gpu_memory`. Docker passes the selected UUIDs to the NVIDIA Container Toolkit. The `ctr` runtime rejects GPU workloads until it has an equivalent device path. A Route can target a Model as it targets an App. This still needs a real NVIDIA host acceptance run before 0.5.0 ships.

- GPU inventory from the NVIDIA management library, including model and free memory.
- An App or Model with `gpus` lands only on a node that has them.
- Use the NVIDIA container toolkit. Tessera does not install drivers.
- A model server is reachable by its Route only from a fitting node.

The host must provide an NVIDIA driver, `nvidia-smi`, Docker, and the NVIDIA Container Toolkit. Tessera does not install or configure any of them. GPU memory is a placement filter based on the most recent inventory, not a hard per-container memory limit.

Acceptance gate: run an actual model container on two NVIDIA hosts with different GPUs. Verify the selected UUID in the Docker device request, a successful Route response, refusal on a non-fitting node, and recovery after the fitting node fails. The local tests cover detection, placement, distinct reservations, and the Docker API request; they cannot prove driver or toolkit behavior without the hardware.

## 0.6.0

Training jobs that survive a better machine showing up. Gang placement is already all-or-nothing. Checkpoint is not.

The topology prerequisite is implemented: agents can report host labels, Apps and Jobs can require `node_labels`, and gang Jobs can require one shared `gang_fabric` value. Checkpoint storage, completion handshake, and safe gang restart remain open. A training Job must not be moved merely because a faster node appears until those pieces are verified.

- A shared checkpoint on NFS or object storage, not a new filesystem.
- Host or fabric network labels for workers that must share a fast link.
- A policy-allowed move checkpoints the job and restarts it on the faster gang. No live migration.

Implementation gate: define a checkpoint contract with a shared URI, a save trigger, and a completion marker written atomically by the worker. The controller waits for every worker in the gang to report a committed checkpoint before stopping any worker. Replacement workers receive the same URI and checkpoint generation; failure leaves the old gang running. The drill uses a shared NFS or S3-compatible target, moves a running two-worker job, and checks that resumed progress is strictly beyond the committed checkpoint. Jobs remain pinned until this handshake exists.

## 0.7.0

Build an image or package for a specific stack, and publish it so other people can pull it. Depends on the 0.2.0 cache. That cache is not the public registry.

The first OCI image path is implemented for App manifests with a `build` stanza. `tessera apply` creates a Dockerfile from the selected base, package manager, packages, source context, and process; tags the local image by its image ID; and optionally pushes it, applying the digest returned by the registry. A disposable local registry drill built, pushed, removed the local copy, pulled, and ran that digest on OrbStack. An isolated CLI drill also built and applied an App to a temporary Tessera controller. Package artifacts, a second-cluster pull drill, and rollback through a published digest remain open. The build runs where the CLI is invoked and requires a working Docker CLI.

- A stack file names the base, the packages, and the process that should be running. Apply it the same way as an App.
- Tessera builds it. The result is an OCI image or a package, not a container that exists only on one node.
- Publish to a public registry the author names. Another cluster pulls that name. There is no private copy step.
- A published name is immutable for that generation. A new build is a new generation, with the same rollback rule as an App.

Implementation gate: accept a build stanza on an App manifest while keeping App as the user resource kind. Build into an OCI image, record its content digest and build inputs with the App generation, then apply the digest-pinned image. Publishing uses explicit registry credentials already configured on the operator's machine. The drill builds once, applies to two nodes through the image cache, pushes to a local registry, pulls on a second Tessera cluster, changes a package version, and proves that rollback selects the prior digest.

## 0.8.0

Use the cloud account you already have. AWS, GCP, and Azure are first. Others follow the same provider interface. A cloud VM is a Node. Do not build this before a LAN node is boring.

The read-only inventory slice is implemented: `tessera cloud inventory` normalizes VM lists from the installed AWS, gcloud, and Azure CLIs, using their existing credentials. Scripted local tests cover all three JSON responses. Provisioning, ownership tracking, adoption, and live account drills remain open. No cloud account was accessed during this work.

- Placement, healing, and moves do not grow a second scheduler.
- Bring your credentials. Tessera does not become the account.
- Start or adopt machines that already have Linux and the agent, including GPU instance types.
- An existing instance can join. You do not have to recreate the account to use it.
- Capacity and score come from the instance, so a faster cloud machine can take a workload the same way a faster LAN machine does.

Implementation gate: use one provider interface for discover, create, inspect, and stop, with idempotency keys and provider instance IDs stored on Nodes. Adopted instances must never be deleted by a Tessera removal. Add AWS, GCP, and Azure adapters with credential passthrough; the agent still reports its own measured resources and joins through the normal token path. Tests use scripted provider responses, followed by opt-in drills in each account that create one disposable VM, join it, place an App, then remove only the created VM. Do not claim provider support from mocks alone.

## 0.9.0

Describe the infrastructure you want. Tessera creates it and keeps it there. This waits on 0.8.0. There is no cloud to reconcile before that.

The pure planning slice is implemented: `tessera infra plan` reads networks, machines, and Apps, compares them with an optional local state snapshot, and prints a stable generation and ordered actions. It proposes potentially destructive changes for confirmation and never proposes deletion of an adopted resource. This is a local preview only. Cloud state discovery, persistent ownership, and apply/reconcile remain open until 0.8.0 provisioning exists.

- One file for networks, machines, and Apps. Apply it the same way as an App.
- The controller reconciles that file against the cloud account: create what is missing, leave what already matches, and show what it will not delete without confirmation.
- A change to the file is a generation, with the same rollback rule as an App.
- This is Tessera's own intent, not a Terraform clone. Import from an existing cloud layout can come after apply works.

Implementation gate: a plan step computes a stable diff before apply. Apply records resource ownership and generation, creates networks before machines, waits for agents to join before Apps, and resumes idempotently after interruption. Any delete or replacement of an adopted resource remains a proposed action requiring `tessera confirm`. The acceptance drill applies the same file twice with no second cloud mutation, changes one machine and App, verifies rollback, and tests interruption after network creation.

## Later

Not scheduled. A machine that already has Linux and the agent does not need this.

- Netboot a prebuilt image that already contains the agent. Time bound is boot time, not a package install.
- This is not a general OS installer.
