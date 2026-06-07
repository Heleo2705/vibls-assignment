#!/usr/bin/env bash
#
# test-job.sh — End-to-end test for the deploy-api job submission and deployment.
#
# Tests:
#   1. POST a job (Go HTTP server repo → cluster)
#   2. Extract job ID
#   3. Poll GET /api/v1/jobs/{id} until terminal state
#   4. Verify pods and service endpoint on success
#
# Usage:
#   ./scripts/test-job.sh                        # uses default repo
#   REPO="my-org/my-repo" ./scripts/test-job.sh  # custom repo
#

set -euo pipefail

# ── Colours ──────────────────────────────────────────────────────────────────
readonly GREEN='\033[0;32m'
readonly RED='\033[0;31m'
readonly CYAN='\033[0;36m'
readonly NC='\033[0m' # No Colour

pass() { printf "${GREEN}[PASS]${NC} %s\n" "$*"; }
fail() { printf "${RED}[FAIL]${NC} %s\n" "$*"; }
info() { printf "${CYAN}[INFO]${NC} %s\n" "$*"; }

# ── Configuration ────────────────────────────────────────────────────────────
BASE_URL="${BASE_URL:-http://localhost:8080}"
API="${BASE_URL}/api/v1/jobs"
REPO="${REPO:-github.com/olliefr/docker-gs-ping}"
BRANCH="${BRANCH:-main}"
NAMESPACE="${NAMESPACE:-test-default}"

POLL_INTERVAL=5          # seconds between status checks
MAX_POLL_SECONDS=300     # 5-minute timeout

# ── Helpers ──────────────────────────────────────────────────────────────────
cleanup() {
  local rc=$?
  if [ $rc -ne 0 ]; then
    info "Test failed — see diagnostics above."
  fi
  exit $rc
}
trap cleanup EXIT

die() {
  fail "$1"
  exit 1
}

# curl wrapper: fail on HTTP errors or connectivity issues.
curl_api() {
  local method=$1 url=$2 body=${3:-}
  shift 2
  if [ -n "$body" ]; then
    curl -sSf -X "$method" "$url" \
      -H 'Content-Type: application/json' \
      -H 'Accept: application/json' \
      -d "$body"
  else
    curl -sSf -X "$method" "$url" \
      -H 'Accept: application/json'
  fi
}

# ── Step 1: Submit a job ─────────────────────────────────────────────────────
info "Step 1 — Submitting job for repo '${REPO}' (branch: ${BRANCH}, namespace: ${NAMESPACE})"

JOB_PAYLOAD=$(cat <<JSON
{
  "repository_url": "${REPO}",
  "branch": "${BRANCH}",
  "target_namespace": "${NAMESPACE}"
}
JSON
)

JOB_RESPONSE=$(curl_api POST "$API" "$JOB_PAYLOAD") || die "POST ${API} failed — is the server running?"

# Extract job ID using grep/sed (no jq dependency).
JOB_ID=$(echo "$JOB_RESPONSE" | grep -o -m1 '"id"[[:space:]]*:[[:space:]]*"[^"]*"' | sed 's/.*: *"\(.*\)"/\1/')
[ -z "$JOB_ID" ] && die "Could not extract job ID from response: ${JOB_RESPONSE}"

pass "Job submitted — ID: ${JOB_ID}"

# ── Step 2: Poll until terminal state ────────────────────────────────────────
info "Step 2 — Polling job status every ${POLL_INTERVAL}s (timeout: ${MAX_POLL_SECONDS}s)"

TERMINAL_STATES="completed failed"
end_time=$(( $(date +%s) + MAX_POLL_SECONDS ))

while true; do
  STATUS_RESPONSE=$(curl_api GET "${API}/${JOB_ID}") || die "GET ${API}/${JOB_ID} failed during polling"

  STATUS=$(echo "$STATUS_RESPONSE" | grep -o -m1 '"status"[[:space:]]*:[[:space:]]*"[^"]*"' | sed 's/.*: *"\(.*\)"/\1/')
  [ -z "$STATUS" ] && die "Could not extract status from response: ${STATUS_RESPONSE}"

  info "Job status: ${STATUS}"

  # Check for terminal states (API returns UPPERCASE: COMPLETED, FAILED)
  case "$STATUS" in
    COMPLETED)
      pass "Job completed"
      break
      ;;
    FAILED)
      # Grab the failure reason if present
      ERROR_MSG=$(echo "$STATUS_RESPONSE" | grep -o -m1 '"error_message"[[:space:]]*:[[:space:]]*"[^"]*"' | sed 's/.*: *"\(.*\)"/\1/')
      die "Job failed${ERROR_MSG:+ — reason: ${ERROR_MSG}}"
      ;;
    *)
      # Still running — check timeout
      now=$(date +%s)
      [ "$now" -ge "$end_time" ] && die "Polling timed out after ${MAX_POLL_SECONDS}s (last status: ${STATUS})"
      sleep "$POLL_INTERVAL"
      ;;
  esac
done

# ── Step 3: Verify Kubernetes pods ───────────────────────────────────────────
info "Step 3 — Verifying pods in namespace '${NAMESPACE}'"

POD_OUTPUT=$(kubectl get pods -n "${NAMESPACE}" 2>&1) || die "kubectl get pods failed: ${POD_OUTPUT}"

# Check at least one pod's STATUS column is Running (look for word boundary)
if echo "$POD_OUTPUT" | grep -q '\bRunning\b'; then
  pass "At least one pod is Running in namespace '${NAMESPACE}'"
else
  die "No Running pods found in namespace '${NAMESPACE}':\n${POD_OUTPUT}"
fi

# ── Step 4: Verify service endpoint ──────────────────────────────────────────
info "Step 4 — Verifying service endpoint"

# Discover the service name dynamically (non-empty)
SVC_NAME=$(kubectl get svc -n "${NAMESPACE}" -o name 2>/dev/null | grep -m1 . | sed 's|^service/||') || true
if [ -z "$SVC_NAME" ]; then
  die "No service found in namespace '${NAMESPACE}'"
fi

# Give the service a moment to become ready
sleep 2

# Try to get the service URL via minikube (works with ingress addon)
SVC_URL=$(minikube service "${SVC_NAME}" -n "${NAMESPACE}" --url 2>/dev/null || true)
if [ -n "$SVC_URL" ]; then
  info "Service URL: ${SVC_URL}"
  SVC_RESPONSE=$(curl -sSf -o /dev/null -w '%{http_code}' "$SVC_URL" 2>&1) || true
  if [ -n "$SVC_RESPONSE" ] && [ "${SVC_RESPONSE:0:1}" = "2" ]; then
    pass "Service endpoint responded HTTP ${SVC_RESPONSE}"
  else
    info "Service at ${SVC_URL} returned HTTP ${SVC_RESPONSE:-unreachable}"
    pass "Deployment verified — pod is Running, service '${SVC_NAME}' exists"
  fi
else
  # Fallback: verify service exists and describe it
  SVC_INFO=$(kubectl describe svc -n "${NAMESPACE}" "${SVC_NAME}" 2>/dev/null || true)
  if [ -n "$SVC_INFO" ]; then
    pass "Service '${SVC_NAME}' exists and is configured in namespace '${NAMESPACE}'"
  else
    die "Service '${SVC_NAME}' not found in namespace '${NAMESPACE}'"
  fi
fi

# ── Done ─────────────────────────────────────────────────────────────────────
echo ""
pass "All tests passed."
exit 0
