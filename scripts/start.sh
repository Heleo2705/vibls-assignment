#!/usr/bin/env bash
set -euo pipefail

# =============================================================================
# start.sh
# --------
# Orchestrates the full local development environment:
#   1. Ensures prerequisites (docker, minikube, kubectl) are installed
#   2. Ensures Minikube is running (reuses setup-minikube.sh)
#   3. Points Docker to Minikube's daemon
#   4. Builds Go binaries (api + worker) — skips gracefully if sources absent
#   5. Starts Docker Compose services — warns if compose file absent
#   6. Waits for all services to become healthy
#   7. Prints service URLs
#
# Usage: ./scripts/start.sh [--timeout SECONDS]
# =============================================================================

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

# Default timeout for health checks (seconds)
HEALTH_TIMEOUT=60

# Parse optional --timeout flag
while [[ $# -gt 0 ]]; do
  case "$1" in
    --timeout)
      HEALTH_TIMEOUT="$2"
      shift 2
      ;;
    --timeout=*)
      HEALTH_TIMEOUT="${1#*=}"
      shift
      ;;
    *)
      # Accept bare number as positional arg for convenience
      if [[ "$1" =~ ^[0-9]+$ ]]; then
        HEALTH_TIMEOUT="$1"
      fi
      shift
      ;;
  esac
done

# ------------------------------------------------------------------
# Helper: print a timestamped message
# ------------------------------------------------------------------
info()  { echo "[INFO]  $(date '+%H:%M:%S')  $*"; }
ok()    { echo "[OK]    $(date '+%H:%M:%S')  $*"; }
warn()  { echo "[WARN]  $(date '+%H:%M:%S')  $*" >&2; }
error() { echo "[ERROR] $(date '+%H:%M:%S')  $*" >&2; }

# ------------------------------------------------------------------
# Step 1 — Check prerequisites
# ------------------------------------------------------------------
info "Checking prerequisites..."

check_command() {
  local cmd="$1"
  local hint="$2"
  if ! command -v "$cmd" &>/dev/null; then
    error "'$cmd' is not installed. $hint"
    exit 1
  fi
  ok "'$cmd' found."
}

check_command "docker"   "Install from https://docs.docker.com/get-docker/"
check_command "minikube" "Install via: brew install minikube"
check_command "kubectl"  "Install via: brew install kubectl"

# ------------------------------------------------------------------
# Step 2 — Ensure Minikube is running
# ------------------------------------------------------------------
info "Checking Minikube status..."

if ! minikube status --format '{{.Host}}' 2>/dev/null | grep -q "Running"; then
  info "Minikube is not running. Delegating to setup-minikube.sh..."
  if [ -f "$SCRIPT_DIR/setup-minikube.sh" ]; then
    bash "$SCRIPT_DIR/setup-minikube.sh"
  else
    info "setup-minikube.sh not found; starting Minikube directly..."
    minikube start --cpus=4 --memory=8192
    minikube addons enable ingress
  fi
else
  ok "Minikube is already running."
fi

# ------------------------------------------------------------------
# Step 3 — Point Docker to Minikube's daemon
# ------------------------------------------------------------------
info "Pointing Docker client to Minikube's daemon..."
eval "$(minikube docker-env)"
ok "Docker is now targeting Minikube's daemon."

# ------------------------------------------------------------------
# Step 4 — Build Go binaries
# ------------------------------------------------------------------
info "Building Go binaries..."

BIN_DIR="$PROJECT_DIR/bin"
mkdir -p "$BIN_DIR"

build_binary() {
  local binary_name="$1"
  local main_path="$2"

  if [ -f "$PROJECT_DIR/$main_path" ]; then
    info "Building '$binary_name' from $main_path ..."
    go build -o "$BIN_DIR/$binary_name" "$PROJECT_DIR/$main_path"
    ok "Built '$binary_name' successfully."
  else
    warn "Source '$main_path' does not exist yet. Skipping build of '$binary_name'."
    warn "  Create the file and re-run this script to build it."
  fi
}

build_binary "api"    "cmd/api/main.go"
build_binary "worker" "cmd/worker/main.go"

# ------------------------------------------------------------------
# Step 5 — Start Docker Compose services
# ------------------------------------------------------------------
COMPOSE_FILE="$PROJECT_DIR/docker/docker-compose.yml"

if [ -f "$COMPOSE_FILE" ]; then
  info "Starting Docker Compose services from $COMPOSE_FILE ..."
  docker compose -f "$COMPOSE_FILE" up -d
  ok "Docker Compose services started."
else
  warn "Docker Compose file not found at '$COMPOSE_FILE'."
  warn "  Skipping service startup. Create the file and re-run this script."
fi

# ------------------------------------------------------------------
# Step 6 — Wait for services to become healthy
# ------------------------------------------------------------------
info "Waiting for services to become healthy (timeout: ${HEALTH_TIMEOUT}s)..."

wait_for_healthy() {
  local service_name="$1"
  local timeout="$2"
  local elapsed=0
  local interval=5

  while [ $elapsed -lt "$timeout" ]; do
    # docker compose ps --status healthy returns output only when healthy
    if docker compose -f "$COMPOSE_FILE" ps --status healthy 2>/dev/null \
         | grep -q "$service_name"; then
      ok "'$service_name' is healthy."
      return 0
    fi
    sleep "$interval"
    elapsed=$((elapsed + interval))
  done

  warn "'$service_name' did not become healthy within ${timeout}s."
  return 1
}

# Only wait if the compose file exists
if [ -f "$COMPOSE_FILE" ]; then
  # Get list of services from the compose file
  SERVICES=$(docker compose -f "$COMPOSE_FILE" config --services 2>/dev/null || true)
  if [ -n "$SERVICES" ]; then
    for svc in $SERVICES; do
      wait_for_healthy "$svc" "$HEALTH_TIMEOUT" || true
    done
  else
    info "No services defined in compose file. Skipping health wait."
  fi
fi

# ------------------------------------------------------------------
# Step 7 — Print URLs
# ------------------------------------------------------------------
echo ""
echo "================================================================"
echo "  Development environment is ready!"
echo ""
echo "  API:       http://localhost:8080"
echo "  Grafana:   http://localhost:3000"
echo "================================================================"
echo ""

exit 0
