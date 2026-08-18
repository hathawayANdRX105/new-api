#!/usr/bin/env bash
set -euo pipefail

# Frontend performance analysis for agent consumption.
# Usage:
#   ./scripts/web-perf.sh bundle    — build + bundle stats JSON
#   ./scripts/web-perf.sh traces    — start dev server, run Chrome traces, output JSON
#   ./scripts/web-perf.sh all       — both

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
WEB_DIR="$ROOT/apps/web"
STATS_DIR="$ROOT/.perf-stats"

mkdir -p "$STATS_DIR"

cmd="${1:-all}"

# ─── Bundle Analysis ─────────────────────────────────────────────
analyze_bundle() {
  echo "=== Building web (production) for bundle analysis ==="
  cd "$WEB_DIR"
  DISABLE_ESLINT_PLUGIN=true VITE_REACT_APP_VERSION="$(cat ../../VERSION)" \
    bun run build 2>&1 | tail -20

  # Generate stats JSON from the build output
  echo "=== Generating bundle stats ==="
  bunx webpack-bundle-analyzer "$WEB_DIR/dist/static/js" \
    --mode json \
    --output "$STATS_DIR/bundle-stats.json" \
    --silent 2>/dev/null || true

  # If webpack-bundle-analyzer didn't work, use rspack stats
  if [ ! -f "$STATS_DIR/bundle-stats.json" ]; then
    echo "=== Falling back to du-based chunk sizes ==="
    {
      echo '{"chunks":['
      first=true
      for f in "$WEB_DIR/dist/static/js/"*.js; do
        [ -f "$f" ] || continue
        size=$(stat -c%s "$f" 2>/dev/null || stat -f%z "$f" 2>/dev/null)
        name=$(basename "$f")
        if [ "$first" = true ]; then
          first=false
        else
          echo ","
        fi
        printf '  {"name":"%s","size":%d}' "$name" "$size"
      done
      echo ""
      echo "],"
      # Total size
      total=$(du -sb "$WEB_DIR/dist/static/js/" 2>/dev/null | cut -f1 || du -sk "$WEB_DIR/dist/static/js/" | awk '{print $1*1024}')
      printf '"total_js_bytes":%s\n' "$total"
      echo "}"
    } > "$STATS_DIR/bundle-stats.json"
  fi

  # Chunk size summary (agent-parseable)
  echo "=== Chunk sizes (bytes) ==="
  {
    echo '{"chunks":['
    first=true
    for f in "$WEB_DIR/dist/static/js/"*.js; do
      [ -f "$f" ] || continue
      size=$(stat -c%s "$f" 2>/dev/null || stat -f%z "$f" 2>/dev/null)
      name=$(basename "$f")
      if [ "$first" = true ]; then
        first=false
      else
        echo ","
      fi
      printf '  {"name":"%s","size":%d}' "$name" "$size"
    done
    echo ""
    echo "]}"
  } | python3 -c "
import json, sys
data = json.load(sys.stdin)
chunks = sorted(data['chunks'], key=lambda x: -x['size'])
total = sum(c['size'] for c in chunks)
print(f'Total JS: {total/1024:.0f} KB across {len(chunks)} chunks')
print()
for c in chunks[:15]:
    print(f'  {c[\"size\"]/1024:7.1f} KB  {c[\"name\"]}')
if len(chunks) > 15:
    print(f'  ... and {len(chunks)-15} more chunks')
" 2>/dev/null || true

  echo ""
  echo "Stats saved to: $STATS_DIR/bundle-stats.json"
}

# ─── Chrome Trace Analysis ────────────────────────────────────────
analyze_traces() {
  echo "=== Starting dev server for trace analysis ==="

  # Start dev server in background
  cd "$WEB_DIR"
  bun run dev -- --port 5180 &
  DEV_PID=$!
  trap "kill $DEV_PID 2>/dev/null" EXIT

  echo "Waiting for dev server to be ready..."
  for i in $(seq 1 30); do
    if curl -s http://localhost:5180 >/dev/null 2>&1; then
      echo "Dev server ready on :5180"
      break
    fi
    sleep 1
  done

  echo "=== Dev server running. Use browser tool to drive Chrome traces. ==="
  echo "PID: $DEV_PID"
  echo "URL: http://localhost:5180"
  echo ""
  echo "Agent: use the browser tool to:"
  echo "  1. open http://localhost:5180"
  echo "  2. Navigate to /channels (login first if needed)"
  echo "  3. Run: tab.evaluate('performance.mark(\"drawer-open-start\")')"
  echo "  4. Click 'Create Channel' button"
  echo "  5. Run: tab.evaluate('performance.mark(\"drawer-open-end\"); performance.measure(\"drawer-open\",\"drawer-open-start\",\"drawer-open-end\"); const m=performance.getEntriesByName(\"drawer-open\"); JSON.stringify({duration:m[0].duration})')"
  echo "  6. Record results to $STATS_DIR/trace-results.json"
  echo ""
  echo "Press Enter to stop dev server..."
  read -r
  kill $DEV_PID 2>/dev/null || true
}

case "$cmd" in
  bundle)
    analyze_bundle
    ;;
  traces)
    analyze_traces
    ;;
  all)
    analyze_bundle
    echo ""
    analyze_traces
    ;;
  *)
    echo "Usage: $0 {bundle|traces|all}"
    exit 1
    ;;
esac
