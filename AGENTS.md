# Tessera

Module `tessera`. One binary: `cmd/tessera`. No Makefile or CI. Verify with `go test ./...`. The acceptance suite is `go test ./internal/drill -count=1` (also `tessera drill`). It needs no Docker.

## Boundaries

- `internal/schedule.Plan` and `internal/survive` are pure. Side effects stay in `controller` and `agent`.
- `agent` imports `controller`. Do not import `agent` from `controller`.
- User kinds are App, Job, Model, Route, Config, Secret, Policy. Node and Assignment are system objects.
- MCP tools in `internal/mcp` are read-only. Mutations go through apply or playbooks.
- `k8simport` converts Deployment, StatefulSet, DaemonSet, Job, Service, Ingress, ConfigMap, and Secret. It does not link client-go. `--from-cluster` shells out to `kubectl`. Report skipped kinds; do not drop them silently.
- Do not add a Go module outside the known set in `ROADMAP.md`: `gopkg.in/yaml.v3`, `github.com/hashicorp/mdns`, `github.com/modelcontextprotocol/go-sdk`, `golang.org/x/sys`, `modernc.org/sqlite`, plus the standard library. A new direct dependency is a roadmap change.

## Behavior that is easy to break

- Healing is `diagnose.Scan` plus `policy.Decide`. Do not put an LLM on that path. `ask` may call `TESSERA_LLM_URL` after the deterministic text.
- `wipe`, `reimage`, and `delete` always return `confirm`, even if listed in `Auto`. They run only from `tessera confirm`.
- A move starts a replacement (`Assignment.Replaces`) and stops the old one only after the replacement is `running`. Do not stop first.
- `api.Active` is true for `failed`. Only `stopped` and `succeeded` drop out of the desired set.
- `PutApp` bumps generation only when `ReleaseEqual` is false. Rollback restores `HealthyGeneration` from `app_history`.
- Heartbeat must not clear `cordoned`. Uncordon happens in `expireNodesLocked` after the move cooldown, and only if heartbeats are fresh.
- Snapshot HMAC is over the stored raw JSON body (`RawSnapshot` / `SnapBody`). Re-marshaling the struct will fail verification.
- Benchmark scores must be finite. `+Inf` breaks heartbeat JSON. Count loop iterations; the accumulator wraps.

## Runtime and process

- `--runtime auto` uses the Docker socket if it exists, else `/var/run/containerd/containerd.sock` plus `ctr`. `ctr` is covered by scripted command tests. Drills and unit tests use `fake`.
- A route listen port stays fixed. Cutover follows a running replacement.
- The image cache is files under the leader data directory, not rows in SQLite. A second start of a cached image does not pull from the registry.
- Join benchmark is stored in the agent cache, refreshed hourly, and shown by `tessera get nodes`.
- Docker list/stop only sees names prefixed `tessera_`.
- `tessera up` re-execs under the watchdog unless `TESSERA_WATCHED=1` or `--watched`. The watchdog does not restart a clean exit or a child that dies within 500ms.
- CLI state is `$TESSERA_DATA` or `~/.tessera`: `client.json` (url, token), `tessera.db`, `agent/cache.json`. Override with `TESSERA_URL` and `TESSERA_TOKEN`.
- mDNS service is `_tessera._tcp`. Advertise failure must not abort startup.
- SQLite driver is `modernc.org/sqlite` (`sqlite`), one connection, WAL. Do not open a second writer.

## Style

Match the tree: no comments, hand-rolled CLI flags rather than cobra. `go test` is the check; there is no separate lint or codegen step. User-facing docs are `README.md`, `CHANGELOG.md`, and `ROADMAP.md` only.
