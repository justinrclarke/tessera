#!/bin/sh
set -eu

lab_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repo_dir=$(CDPATH= cd -- "$lab_dir/.." && pwd)
compose_file="$lab_dir/compose.yaml"

for command_name in docker kind kubectl go curl; do
    command -v "$command_name" >/dev/null || { echo "$command_name is required" >&2; exit 1; }
done

if [ -n "$(docker compose -f "$compose_file" --profile services --profile load ps -q)" ]; then
    echo "Stop the existing Tessera lab before running the Kubernetes drill" >&2
    exit 1
fi

temp_dir=$(mktemp -d)
cluster_name="tessera-bridge-$(basename "$temp_dir" | tr '[:upper:]' '[:lower:]' | tr -cd 'a-z0-9' | cut -c1-12)"
kubeconfig="$temp_dir/kubeconfig"
if kind get clusters | grep -Fxq "$cluster_name"; then
    echo "Temporary kind cluster name already exists: $cluster_name" >&2
    exit 1
fi
cleanup() {
    sh "$lab_dir/lab.sh" clean || true
    kind delete cluster --name "$cluster_name" || true
    rm -rf "$temp_dir"
}
trap cleanup 0

kind create cluster --name "$cluster_name" --kubeconfig "$kubeconfig" --wait 90s
kubectl --kubeconfig "$kubeconfig" create namespace bridge
kubectl --kubeconfig "$kubeconfig" -n bridge create deployment bridge-api --image=tessera-lab-app:local --port=8080
kubectl --kubeconfig "$kubeconfig" -n bridge expose deployment bridge-api --port=8089 --target-port=8080

(cd "$repo_dir" && go build -o "$temp_dir/tessera" ./cmd/tessera)
remaining=60
while [ "$remaining" -gt 0 ]; do
    diagnosis=$("$temp_dir/tessera" ask --kubeconfig "$kubeconfig" --namespace bridge "why is bridge-api failing")
    if printf '%s\n' "$diagnosis" | grep -Eq 'ErrImagePull|ImagePullBackOff'; then
        printf '%s\n' "$diagnosis"
        break
    fi
    sleep 1
    remaining=$((remaining - 1))
done
[ "$remaining" -gt 0 ] || { echo "Kubernetes image pull failure was not diagnosed" >&2; exit 1; }

(cd "$repo_dir" && sh "$lab_dir/lab.sh" up)
"$temp_dir/tessera" import --from-cluster --kubeconfig "$kubeconfig" --namespace bridge --dry-run > "$temp_dir/import.yaml"
grep -q '^name: bridge-api$' "$temp_dir/import.yaml"
grep -q '^kind: Route$' "$temp_dir/import.yaml"
(cd "$repo_dir" && sh "$lab_dir/lab.sh" apply "$temp_dir/import.yaml")
docker compose -f "$compose_file" exec -T controller sh -c '
    remaining=90
    while [ "$remaining" -gt 0 ]; do
        if wget -qO- http://127.0.0.1:8089/health; then
            exit 0
        fi
        sleep 1
        remaining=$((remaining - 1))
    done
    exit 1
'
echo
echo "Kubernetes diagnosis, import, and Tessera Route passed"
