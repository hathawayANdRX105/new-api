WEB_DIR := "./apps/web"
API_DIR := "./apps/api"
API_EMBED_DIR := "./apps/api/web/dist"
GO_BIN_CACHE := env_var_or_default("GO_BIN_CACHE", env_var("HOME") + "/.cache/new-api-bin")
DEV_WEB_PORT := env_var_or_default("DEV_WEB_PORT", "5173")
DEV_COMPOSE_FILE := "deploy/docker-compose.dev.yml"
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
