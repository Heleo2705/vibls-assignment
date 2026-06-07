#!/usr/bin/env bash
set -euo pipefail

# =============================================================================
# setup-minikube.sh
# ----------------
# Checks for Minikube, starts it if not running, enables ingress addon,
# and verifies the cluster is reachable via kubectl.
#
# Usage: ./scripts/setup-minikube.sh
# =============================================================================

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

# --- Step 1: Check if Minikube is installed ---
if ! command -v minikube &>/dev/null; then
  echo "[ERROR] minikube is not installed. Please install it first:"
  echo "  brew install minikube"
  echo "  or visit https://minikube.sigs.k8s.io/docs/start/"
  exit 1
fi
echo "[OK] minikube found: $(minikube version --short 2>/dev/null || echo 'version unknown')"

# --- Step 2: Check if kubectl is available ---
if ! command -v kubectl &>/dev/null; then
  echo "[ERROR] kubectl is not installed. Please install it first."
  exit 1
fi
echo "[OK] kubectl found"

# --- Step 3: Check if Minikube is already running ---
echo "[INFO] Checking Minikube status..."
MINIKUBE_STATUS="$(minikube status --format '{{.Host}}' 2>/dev/null || echo 'missing')"

if [ "$MINIKUBE_STATUS" = "Running" ]; then
  echo "[OK] Minikube is already running."
else
  # --- Step 4: Start Minikube ---
  echo "[INFO] Minikube is not running. Starting with 4 CPUs and 8 GB RAM..."
  minikube start --cpus=4 --memory=8192
  echo "[OK] Minikube started successfully."
fi

# --- Step 5: Enable ingress addon ---
echo "[INFO] Enabling ingress addon..."
minikube addons enable ingress
echo "[OK] Ingress addon enabled."

# --- Step 6: Print docker-env instructions ---
echo ""
echo "================================================================"
echo "  IMPORTANT: Point your Docker client to Minikube's daemon:"
echo ""
echo "    eval \$(minikube docker-env)"
echo ""
echo "  Run the above in every terminal where you build Docker images."
echo "================================================================"
echo ""

# --- Step 7: Verify kubectl can reach the cluster ---
echo "[INFO] Verifying cluster connectivity via kubectl..."
if kubectl get nodes >/dev/null 2>&1; then
  echo "[OK] Cluster is reachable. Nodes:"
  kubectl get nodes
else
  echo "[ERROR] kubectl cannot reach the cluster."
  exit 1
fi

# --- Done ---
echo ""
echo "[SUCCESS] Minikube is ready for use."
exit 0
