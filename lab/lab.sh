#!/bin/sh
set -eu

lab_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
compose_file="$lab_dir/compose.yaml"
demo_image=tessera-lab-app:local
route_url=http://127.0.0.1:8088

compose() {
    docker compose -f "$compose_file" "$@"
}

wait_route() {
    remaining=90
    while [ "$remaining" -gt 0 ]; do
        if curl -fsS "$route_url/health" >/dev/null 2>&1; then
            return 0
        fi
        sleep 1
        remaining=$((remaining - 1))
    done
    echo "Route did not become healthy within 90 seconds" >&2
    return 1
}

wait_nodes() {
    remaining=90
    while [ "$remaining" -gt 0 ]; do
        nodes=$(compose exec -T controller tessera get nodes)
        if printf '%s\n' "$nodes" | grep -q '^lab-a ' && printf '%s\n' "$nodes" | grep -q '^lab-b '; then
            return 0
        fi
        sleep 1
        remaining=$((remaining - 1))
    done
    echo "Both lab nodes did not join within 90 seconds" >&2
    return 1
}

wait_cache() {
    remaining=90
    while [ "$remaining" -gt 0 ]; do
        if curl -fsSI -H 'Authorization: Bearer tessera-local-lab-token' \
            'http://127.0.0.1:7468/v1/images?ref=tessera-lab-app%3Alocal' >/dev/null 2>&1; then
            return 0
        fi
        sleep 1
        remaining=$((remaining - 1))
    done
    echo "Sample image did not reach the controller cache within 90 seconds" >&2
    return 1
}

wait_dependencies() {
    remaining=90
    while [ "$remaining" -gt 0 ]; do
        if curl -fsS "$route_url/dependencies" 2>/dev/null; then
            echo
            return 0
        fi
        sleep 1
        remaining=$((remaining - 1))
    done
    echo "Optional services were not reachable by name from the Tessera app" >&2
    return 1
}

assignment() {
    compose exec -T controller tessera get assignments | awk '
        /app=lab-api/ && /status=running/ {
            id=$1
            node=""
            for (i=1; i<=NF; i++) if ($i ~ /^node=/) { split($i, part, "="); node=part[2] }
            if (node != "") print id, node
        }
    ' | tail -n 1
}

load_image() {
    image=${1:-$demo_image}
    docker image inspect "$image" >/dev/null
    docker save "$image" | compose exec -T node-a-docker docker load
    docker save "$image" | compose exec -T node-b-docker docker load
}

load_seed_image() {
    docker image inspect "$demo_image" >/dev/null
    docker save "$demo_image" | compose exec -T node-a-docker docker load
}

run_load() {
    scenario=${1:-smoke}
    case "$scenario" in
        smoke|baseline|spike|soak|failure) ;;
        *) echo "Unknown k6 scenario: $scenario" >&2; return 2 ;;
    esac
    mkdir -p "$lab_dir/results"
    compose run --rm -e "SCENARIO=$scenario" -e "BASE_URL=http://controller:8088" k6 \
        run --summary-export "/results/$scenario.json" /scripts/traffic.js
}

show_status() {
    compose ps
    compose exec -T controller tessera get nodes
    compose exec -T controller tessera get apps
    compose exec -T controller tessera get assignments
    compose exec -T controller tessera get actions
}

show_endpoints() {
    for service in registry postgres redis object-store; do
        container_id=$(compose ps -q "$service")
        if [ -n "$container_id" ]; then
            address=$(docker inspect --format '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$container_id")
            case "$service" in
                registry) port=5000 ;;
                postgres) port=5432 ;;
                redis) port=6379 ;;
                object-store) port=9000 ;;
            esac
            printf '%s %s:%s\n' "$service" "$address" "$port"
        fi
    done
}

inject() {
    curl -fsS -X POST "$route_url/control?delay_ms=$1&fail_every=$2"
    echo
}

run_scenario() {
    name=${1:-}
    case "$name" in
        crash)
            current=$(assignment)
            set -- $current
            [ "$#" -eq 2 ] || { echo "No running lab assignment" >&2; return 1; }
            suffix=${2#lab-}
            compose exec -T "node-$suffix-docker" docker kill "tessera_$1"
            wait_route
            show_status
            ;;
        node-loss)
            current=$(assignment)
            set -- $current
            [ "$#" -eq 2 ] || { echo "No running lab assignment" >&2; return 1; }
            suffix=${2#lab-}
            started_at=$(date +%s)
            compose stop "node-$suffix" "node-$suffix-dns" "node-$suffix-docker"
            wait_route
            recovered_at=$(date +%s)
            echo "Route recovered in $((recovered_at - started_at)) seconds"
            show_status
            echo "Restore the stopped node with: lab/lab.sh recover $suffix"
            ;;
        latency)
            inject 750 0
            if run_load baseline; then
                inject 0 0
                echo "Expected the p95 latency threshold to fail" >&2
                return 1
            fi
            inject 0 0
            echo "Latency fault crossed the k6 threshold as expected"
            ;;
        errors)
            inject 0 2
            if ! run_load failure; then
                inject 0 0
                return 1
            fi
            inject 0 0
            ;;
        confirm-delete)
            curl -fsS -X POST http://127.0.0.1:7468/v1/act \
                -H 'Authorization: Bearer tessera-local-lab-token' \
                -H 'Content-Type: application/json' \
                -d '{"kind":"delete","target":"lab-api","reason":"lab confirmation"}' >/dev/null
            action_id=$(compose exec -T controller tessera confirm | awk '$2 == "delete" && $3 == "lab-api" { id=$1 } END { print id }')
            [ -n "$action_id" ] || { echo "No proposed delete action" >&2; return 1; }
            compose exec -T controller tessera confirm "$action_id"
            compose exec -T controller tessera apply -f /lab/app.yaml
            wait_route
            show_status
            ;;
        *)
            echo "Scenario must be crash, node-loss, latency, errors, or confirm-delete" >&2
            return 2
            ;;
    esac
}

case "${1:-}" in
    up)
        compose up -d --build controller node-a-docker node-a-dns node-a
        docker build -t "$demo_image" -f "$lab_dir/demo/Dockerfile" "$lab_dir/demo"
        load_seed_image
        compose exec -T controller tessera apply -f /lab/app.yaml
        wait_route
        wait_cache
        compose up -d node-b-docker node-b-dns node-b
        wait_nodes
        show_status
        echo "Sample route: $route_url/work"
        ;;
    load)
        run_load "${2:-smoke}"
        ;;
    scenario)
        run_scenario "${2:-}"
        ;;
    recover)
        suffix=${2:-}
        case "$suffix" in a|b) ;; *) echo "Recover a or b" >&2; exit 2 ;; esac
        compose up -d "node-$suffix-docker" "node-$suffix-dns" "node-$suffix"
        wait_route
        show_status
        ;;
    reset)
        inject 0 0
        ;;
    status)
        show_status
        ;;
    services)
        compose --profile services up -d registry postgres redis object-store
        wait_dependencies
        show_endpoints
        ;;
    endpoints)
        show_endpoints
        ;;
    verify)
        compose --profile services --profile load down -v --remove-orphans
        trap 'compose --profile services --profile load down -v --remove-orphans' 0
        sh "$lab_dir/lab.sh" up
        sh "$lab_dir/lab.sh" load smoke
        sh "$lab_dir/lab.sh" services
        if compose exec -T node-b-docker docker image inspect "$demo_image" >/dev/null 2>&1; then
            echo "The second node already has the sample image; cache transfer was not tested" >&2
            exit 1
        fi
        echo "Confirmed the second node has no sample image before failover"
        sh "$lab_dir/lab.sh" scenario crash
        active=$(assignment)
        set -- $active
        [ "$#" -eq 2 ] || { echo "No running lab assignment" >&2; exit 1; }
        stopped_node=${2#lab-}
        sh "$lab_dir/lab.sh" scenario node-loss
        compose exec -T node-b-docker docker image inspect "$demo_image" >/dev/null
        echo "Confirmed the second node received the sample image through Tessera"
        wait_dependencies
        sh "$lab_dir/lab.sh" recover "$stopped_node"
        sh "$lab_dir/lab.sh" scenario errors
        sh "$lab_dir/lab.sh" scenario latency
        sh "$lab_dir/lab.sh" scenario confirm-delete
        ;;
    load-image)
        [ "$#" -eq 2 ] || { echo "Usage: lab/lab.sh load-image IMAGE" >&2; exit 2; }
        load_image "$2"
        ;;
    apply)
        [ "$#" -eq 2 ] || { echo "Usage: lab/lab.sh apply MANIFEST" >&2; exit 2; }
        compose exec -T controller tessera apply -f - < "$2"
        ;;
    down)
        compose down
        ;;
    clean)
        compose --profile services --profile load down -v --remove-orphans
        ;;
    *)
        echo "Usage: lab/lab.sh {up|status|load [smoke|baseline|spike|soak|failure]|scenario [crash|node-loss|latency|errors|confirm-delete]|recover [a|b]|reset|services|endpoints|load-image IMAGE|apply MANIFEST|verify|down|clean}" >&2
        exit 2
        ;;
esac
