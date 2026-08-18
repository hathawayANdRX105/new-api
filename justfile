WEB_DIR := justfile_directory() + "/apps/web"
API_DIR := justfile_directory() + "/apps/api"
API_EMBED_DIR := justfile_directory() + "/apps/api/web/dist"
GO_BIN_CACHE := env_var_or_default("GO_BIN_CACHE", env_var("HOME") + "/.cache/new-api-bin")
DEV_WEB_PORT := env_var_or_default("DEV_WEB_PORT", "5173")
DEV_COMPOSE_FILE := justfile_directory() + "/deploy/docker-compose.dev.yml"
DEV_POSTGRES_SERVICE := "postgres"
DEV_API_SERVICE := "new-api"
DEV_POSTGRES_DB := "new-api"
DEV_POSTGRES_USER := "root"
DEV_SQLITE_PATH := env_var_or_default("SQLITE_PATH", "one-api.db")

# default: build web + start api
all: build-all-web start-api

# Build web frontend and copy into api package for go:embed
build-web:
    #!/usr/bin/env bash
    set -euo pipefail
    echo "Building web frontend..."
    cd "{{ WEB_DIR }}" && bun install --frozen-lockfile
    cd "{{ WEB_DIR }}" && DISABLE_ESLINT_PLUGIN='true' VITE_REACT_APP_VERSION="$(cat ../../VERSION)" bun run build
    echo "Copying web dist into api package (embed 方案 A)..."
    rm -rf "{{ API_EMBED_DIR }}"
    mkdir -p "{{ API_EMBED_DIR }}"
    cp -r "{{ WEB_DIR }}/dist/." "{{ API_EMBED_DIR }}/"

build-all-web: build-web

# Remove reproducible frontend build output only
clean-web:
    gio trash --force "{{ WEB_DIR }}/dist" "{{ API_EMBED_DIR }}"

# Start api dev server (background)
start-api:
    cd "{{ API_DIR }}" && go run main.go &

# Start api dev server with incremental build cache
run-api:
    #!/usr/bin/env bash
    set -euo pipefail
    branch="$(git branch --show-current 2>/dev/null || echo 'detached')"
    bin_name="new-api-$(echo "$branch" | tr '/' '-')"
    mkdir -p "{{ GO_BIN_CACHE }}"
    cd "{{ API_DIR }}" && GOWORK=off go build -o "{{ GO_BIN_CACHE }}/$bin_name" .
    "{{ GO_BIN_CACHE }}/$bin_name"

# Start only docker dev database (postgres). Redis is expected on localhost:6379
# (either a host Redis or the docker dev redis — see dev-db-full if you need both in Docker).
dev-db:
    docker compose -f "{{ DEV_COMPOSE_FILE }}" up -d "{{ DEV_POSTGRES_SERVICE }}"

# Start postgres + redis both in Docker (use if no host Redis is running on :6379)
dev-db-full:
    docker compose -f "{{ DEV_COMPOSE_FILE }}" up -d "{{ DEV_POSTGRES_SERVICE }}" redis

# Start Go API binary locally (no Docker API container). Requires dev-db running.
# Uses branch-scoped binary cache so multiple worktrees can coexist.
start-wt:
    #!/usr/bin/env bash
    set -euo pipefail
    branch="$(git branch --show-current 2>/dev/null || echo 'detached')"
    bin_name="new-api-$(echo "$branch" | tr '/' '-')"
    mkdir -p "{{ GO_BIN_CACHE }}"
    echo "Building Go binary for branch: $branch..."
    cd "{{ API_DIR }}" && GOWORK=off go build -o "{{ GO_BIN_CACHE }}/$bin_name" .
    echo "Starting API: {{ GO_BIN_CACHE }}/$bin_name  (port 3000)"
    SQL_DSN="postgresql://root:123456@localhost:5432/new-api" \
    REDIS_CONN_STRING="redis://localhost:6379" \
    SESSION_COOKIE_SECURE=false \
    TZ=Asia/Shanghai \
    "{{ GO_BIN_CACHE }}/$bin_name"

# Full worktree dev: docker DBs + local Go API binary + web HMR (all background)
# This is the recommended entry point for agent-driven UI/API development.
dev-wt: dev-db
    #!/usr/bin/env bash
    set -euo pipefail
    echo "Starting Go API binary in background..."
    just start-wt &
    API_PID=$!
    sleep 1
    echo "Starting web dev server (HMR)..."
    just dev-web &
    WEB_PID=$!
    echo ""
    echo "=== Worktree dev environment ready ==="
    echo "  API:  http://localhost:3000"
    echo "  Web:  http://localhost:{{ DEV_WEB_PORT }}"
    echo "  (web HMR proxies /api to API)"
    echo ""
    echo "Press Ctrl+C to stop both."
    trap 'kill $API_PID $WEB_PID 2>/dev/null' EXIT INT TERM
    wait

# Rebuild and restart docker dev api service
dev-api-rebuild:
    docker compose -f "{{ DEV_COMPOSE_FILE }}" up -d --build "{{ DEV_API_SERVICE }}"
 
# Start docker dev api services (postgres + redis + api container)
dev-api:
    docker compose -f "{{ DEV_COMPOSE_FILE }}" up -d

# Start web frontend dev server
dev-web:
     #!/usr/bin/env bash
     set -euo pipefail
     echo "Web frontend: http://localhost:{{ DEV_WEB_PORT }}"
     cd "{{ WEB_DIR }}" && bun install
     cd "{{ WEB_DIR }}" && bun run dev -- --host 0.0.0.0 --port "{{ DEV_WEB_PORT }}"
 
# Start both docker api and web dev servers (legacy: uses Docker API container)
dev: dev-api dev-web

# Deploy the official Karmada Dashboard against an existing Karmada host cluster.
# Metrics are disabled to limit local resource use. The official web, Karmada API,
# and member-cluster API remain separate single-replica workloads.
KARMADA_CONTEXT := env_var_or_default("KARMADA_CONTEXT", "kind-karmada-host")
KARMADA_NAMESPACE := env_var_or_default("KARMADA_NAMESPACE", "karmada-system")
KARMADA_DASHBOARD_MANIFEST := justfile_directory() + "/deploy/k8s/karmada-dashboard/local.yaml"
KARMADA_DASHBOARD_RBAC := justfile_directory() + "/deploy/k8s/karmada-dashboard/rbac.yaml"
KARMADA_CONFIG_SECRET := env_var_or_default("KARMADA_CONFIG_SECRET", "karmada-controller-manager-config")
KARMADA_API_SERVER := env_var_or_default("KARMADA_API_SERVER", "")
KARMADA_DASHBOARD_NODE_URL := env_var_or_default("KARMADA_DASHBOARD_NODE_URL", "http://172.25.0.2:32000/karmada-dashboard/")

karmada-dashboard-local:
    #!/usr/bin/env bash
    set -euo pipefail
    : "${KARMADA_API_SERVER:?KARMADA_API_SERVER is required (export it, e.g. KARMADA_API_SERVER=https://karmada-apiserver:5443)}"
    context="{{ KARMADA_CONTEXT }}"
    namespace="{{ KARMADA_NAMESPACE }}"
    tmp_config="$(mktemp)"
    tmp_host_config="$(mktemp)"
    trap 'rm -f "$tmp_config" "$tmp_host_config"' EXIT
    kubectl --context "$context" get nodes >/dev/null
    kubectl --context "$context" -n "$namespace" get secret "{{ KARMADA_CONFIG_SECRET }}" \
        -o jsonpath='{.data.karmada\.config}' | base64 -d > "$tmp_config"
    cp "$tmp_config" "$tmp_host_config"
    sed -i "s#https://karmada-apiserver.karmada-system.svc.cluster.local:5443#{{ KARMADA_API_SERVER }}#" "$tmp_host_config"
    kubectl --context "$context" -n "$namespace" create secret generic karmada-dashboard-config \
        --from-file=karmada.config="$tmp_config" \
        --dry-run=client -o yaml | kubectl --context "$context" apply -f -
    KUBECONFIG="$tmp_host_config" kubectl --context karmada-admin apply -f "{{ KARMADA_DASHBOARD_RBAC }}"
    kubectl --context "$context" apply -f "{{ KARMADA_DASHBOARD_MANIFEST }}"
    kubectl --context "$context" -n "$namespace" rollout status deploy/karmada-dashboard-api --timeout=5m
    kubectl --context "$context" -n "$namespace" rollout status deploy/kubernetes-dashboard-api --timeout=5m
    kubectl --context "$context" -n "$namespace" rollout status deploy/karmada-dashboard-web --timeout=5m
    kubectl --context "$context" -n "$namespace" get svc karmada-dashboard-web >/dev/null
    dashboard_url="{{ KARMADA_DASHBOARD_NODE_URL }}"
    curl --fail --silent --show-error "$dashboard_url" >/dev/null
    echo "Karmada Dashboard NodePort: $dashboard_url"
    echo "Start New API with KARMADA_DASHBOARD_URL=$dashboard_url and KARMADA_DASHBOARD_TOKEN set server-side."
    just karmada-dashboard-status

# Forward the official Dashboard web service for the New API iframe.
karmada-dashboard-forward:
    kubectl --context "{{ KARMADA_CONTEXT }}" -n "{{ KARMADA_NAMESPACE }}" port-forward service/karmada-dashboard-web 18000:8000

# Print a short-lived Dashboard login token. Never put it in frontend configuration or URLs.
karmada-dashboard-token:
    #!/usr/bin/env bash
    set -euo pipefail
    : "${KARMADA_API_SERVER:?KARMADA_API_SERVER is required (export it, e.g. KARMADA_API_SERVER=https://karmada-apiserver:5443)}"
    tmp_config="$(mktemp)"
    trap 'rm -f "$tmp_config"' EXIT
    kubectl --context "{{ KARMADA_CONTEXT }}" -n "{{ KARMADA_NAMESPACE }}" get secret "{{ KARMADA_CONFIG_SECRET }}" \
        -o jsonpath='{.data.karmada\.config}' | base64 -d > "$tmp_config"
    sed -i "s#https://karmada-apiserver.karmada-system.svc.cluster.local:5443#{{ KARMADA_API_SERVER }}#" "$tmp_config"
    KUBECONFIG="$tmp_config" kubectl --context karmada-admin -n "{{ KARMADA_NAMESPACE }}" get secret karmada-dashboard-token \
        -o jsonpath='{.data.token}' | base64 -d

karmada-dashboard-status:
    kubectl --context "{{ KARMADA_CONTEXT }}" -n "{{ KARMADA_NAMESPACE }}" get pods,svc -l 'app in (karmada-dashboard-api,kubernetes-dashboard-api,karmada-dashboard-web)'

karmada-dashboard-clean:
    #!/usr/bin/env bash
    set -euo pipefail
    : "${KARMADA_API_SERVER:?KARMADA_API_SERVER is required (export it, e.g. KARMADA_API_SERVER=https://karmada-apiserver:5443)}"
    tmp_config="$(mktemp)"
    trap 'rm -f "$tmp_config"' EXIT
    kubectl --context "{{ KARMADA_CONTEXT }}" -n "{{ KARMADA_NAMESPACE }}" get secret "{{ KARMADA_CONFIG_SECRET }}" \
        -o jsonpath='{.data.karmada\.config}' | base64 -d > "$tmp_config"
    sed -i "s#https://karmada-apiserver.karmada-system.svc.cluster.local:5443#{{ KARMADA_API_SERVER }}#" "$tmp_config"
    kubectl --context "{{ KARMADA_CONTEXT }}" delete -f "{{ KARMADA_DASHBOARD_MANIFEST }}" --ignore-not-found
    kubectl --context "{{ KARMADA_CONTEXT }}" -n "{{ KARMADA_NAMESPACE }}" delete secret karmada-dashboard-config --ignore-not-found
    KUBECONFIG="$tmp_config" kubectl --context karmada-admin delete -f "{{ KARMADA_DASHBOARD_RBAC }}" --ignore-not-found

# Run Go tests (api + relaykit)
test:
    #!/usr/bin/env bash
    set -euo pipefail
    echo "Testing api Go module..."
    cd "{{ API_DIR }}"
    root_module="$(GOWORK=off go list -m)"
    root_packages="$(GOWORK=off go list -e ./... | grep -vxF "$root_module")"
    GOWORK=off go test $root_packages
    echo "Testing relaykit Go module..."
    cd modules/relaykit && GOWORK=off go test ./...

# Reset local setup wizard state (postgres or sqlite)
reset-setup:
    #!/usr/bin/env bash
    set -euo pipefail
    if docker compose -f "{{ DEV_COMPOSE_FILE }}" ps --services --status running | grep -qx "{{ DEV_POSTGRES_SERVICE }}"; then
        echo "Detected running docker dev PostgreSQL. Removing setup record and root users..."
        docker compose -f "{{ DEV_COMPOSE_FILE }}" exec -T {{ DEV_POSTGRES_SERVICE }} \
            psql -U {{ DEV_POSTGRES_USER }} -d {{ DEV_POSTGRES_DB }} \
            -c 'DELETE FROM setups;' \
            -c 'DELETE FROM users WHERE role = 100;' \
            -c "DELETE FROM options WHERE key IN ('SelfUseModeEnabled', 'DemoSiteEnabled');"
        echo "Restarting docker dev api so setup status is recalculated..."
        docker compose -f "{{ DEV_COMPOSE_FILE }}" restart {{ DEV_API_SERVICE }}
    else
        db_path="${SQLITE_PATH:-{{ DEV_SQLITE_PATH }}}"
        db_path="${db_path%%\?*}"
        if [ -f "$db_path" ]; then
            echo "Detected local SQLite database: $db_path"
            sqlite3 "$db_path" \
                "DELETE FROM setups; DELETE FROM users WHERE role = 100; DELETE FROM options WHERE key IN ('SelfUseModeEnabled', 'DemoSiteEnabled');"
            echo "SQLite setup state reset. Restart the local api process before testing the setup wizard."
        else
            echo "No running docker dev PostgreSQL or local SQLite database found."
            echo "Start the dev stack with 'just dev-api', or set SQLITE_PATH/DEV_SQLITE_PATH to your local SQLite database."
            exit 1
        fi
    fi
