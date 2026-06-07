# deploy-api

A self-service deployment pipeline that accepts deployment jobs as JSON, clones Git repos, builds Docker images, pushes to a registry, generates Kubernetes manifests, and deploys to an embedded k3s cluster — all with a single `docker compose up`.

## Architecture

```
┌─────────┐     POST /api/v1/jobs     ┌─────────┐
│  curl   │ ────────────────────────► │   API   │
│  user   │                           │  :8080  │
└─────────┘                           └────┬────┘
                                           │ saves job
                                           ▼
                                      ┌─────────┐     ┌─────────┐
                                      │ Postgres │ ──► │  Redis  │
                                      │  jobs    │     │  queue  │
                                      └─────────┘     └────┬────┘
                                                            │ picks up
                                                            ▼
                                                      ┌──────────┐
                                                      │  Worker  │
                                                      │ (dind)   │
                                                      └────┬─────┘
                                                           │
              ┌──────────┬──────────┬──────────┬───────────┼───────────┬──────────┐
              ▼          ▼          ▼          ▼           ▼           ▼          ▼
           Clone     Build      Push      Manifest     Verify      Apply     Health
         (go-git)  (docker)  (docker)   (templates) (kubeconform +  (k8s Go   (poll pods)
                                                   trivy + OPA)     client)    │
                                                                              ▼
                                                                         ┌──────┐
                                                                         │ k3s  │
                                                                         │ K8s  │
                                                                         └──────┘
```

## Quick Start — One Command

**Prerequisites:** [Docker Desktop](https://www.docker.com/products/docker-desktop/) (4.30+)

```bash
cd deploy-api
docker compose -f docker/docker-compose.yml --env-file docker/.env up -d --build
```

Wait ~2 minutes for k3s to initialize. Verify everything is up:

```bash
docker compose -f docker/docker-compose.yml ps --services
# Expected: api grafana k3s loki postgres prometheus promtail redis registry tempo worker

curl http://localhost:8080/healthz
# {"status":"ok"}

docker inspect --format='{{.State.Health.Status}}' deploy-api-k3s-1
# healthy
```

## Deploy Your First App

```bash
cd deploy-api

# 1. Submit a job
JOB_ID=$(curl -s -X POST http://localhost:8080/api/v1/jobs \
  -H "Authorization: Bearer admin-token" \
  -H "Content-Type: application/json" \
  -d @scripts/sample-job.json | python3 -c "import sys,json; print(json.load(sys.stdin)['id'])")

echo "Job: $JOB_ID"

# 2. Watch the pipeline progress (7 stages, ~2 minutes)
watch -n 10 "curl -s http://localhost:8080/api/v1/jobs/$JOB_ID \
  -H 'Authorization: Bearer admin-token' | python3 -m json.tool"

# 3. See the deployed app in k3s
docker exec deploy-api-k3s-1 kubectl get all -n sandbox
```

## API Reference

### POST /api/v1/jobs — Create deployment job

**Auth:** Bearer token in `Authorization` header.

| Token | Role | Permissions |
|-------|------|-------------|
| `admin-token` | admin | create, list, view — all namespaces |
| `dev-token` | developer | create, list, view — sandbox, staging |
| `view-token` | viewer | list, view — all namespaces |

**Request:**

```json
{
  "repository_url":    "https://github.com/olliefr/docker-gs-ping",  // required
  "branch":            "main",
  "build_context":     ".",
  "dockerfile_path":   "Dockerfile",
  "target_namespace":  "sandbox",                                    // required
  "version":           "1",                                          // for idempotency + force-retry
  "resource_overrides": {                                            // optional
    "cpu_cores": 0.5,
    "memory_mb": 256,
    "replicas": 1
  }
}
```

**Responses:**

| Status | Body | Meaning |
|--------|------|---------|
| `201` | `{ "id": "...", "status": "QUEUED" }` | Job created |
| `201` | (same shape, cached) | Idempotent replay — identical request within 24h |
| `409` | `{ "error": "active deployment exists", "existing_job_id": "..." }` | Job already in progress for this target |
| `400` | `{ "error": "..." }` | Missing required fields |

**Idempotency:** Jobs are deduplicated by SHA-256 of `{repo_url, branch, target_namespace, build_context, dockerfile_path, resource_overrides, version}`. Change the `version` field to force a new deployment.

### GET /api/v1/jobs — List jobs

Query params: `?status=QUEUED&namespace=sandbox&limit=10&offset=0`

### GET /api/v1/jobs/{id} — Get job status

Tracks `status` (QUEUED → RUNNING → COMPLETED/FAILED) and `current_stage` (CLONING → BUILDING → PUSHING → MANIFEST_GENERATING → VERIFYING → APPLYING → HEALTH_CHECKING). Each stage transition is recorded in the `job_events` audit trail.

### GET /healthz — Liveness

### GET /metrics — Prometheus

## Pipeline Stages

| Stage | Time | Tool | What it does |
|-------|------|------|-------------|
| **CLONING** | ~1s | go-git | Shallow clone the repo to `/tmp/workspaces/{jobID}/` |
| **BUILDING** | ~5s | docker build | Build Docker image, tag as `registry:5000/{ns}:{jobID}` |
| **PUSHING** | ~2s | docker push | Push to internal registry |
| **MANIFEST_GENERATING** | <1ms | Go templates | Render deployment, service, ingress, HPA YAML |
| **VERIFYING** | ~10s | kubeconform + trivy + OPA | Validate YAML schema, scan image vulns, check labels, verify resource bounds |
| **APPLYING** | ~1s | k8s.io/client-go | Server-side apply to k3s, auto-create namespace |
| **HEALTH_CHECKING** | ~30s | k8s.io/client-go | Poll pods until Ready, check service endpoints |

## Generated Manifests (Example)

When a job with ID `abc123` deploys to namespace `demo`, the following manifests are generated:

**deployment.yaml:**
```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: app-abc123
  namespace: demo
  labels:
    app: app-abc123
    managed-by: deploy-api
spec:
  replicas: 1
  selector:
    matchLabels:
      app: app-abc123
  template:
    metadata:
      labels:
        app: app-abc123
    spec:
      containers:
      - name: app-abc123
        image: registry:5000/demo:abc123
        ports:
        - containerPort: 8080
        resources:
          requests:
            cpu: 100m
            memory: 128Mi
          limits:
            cpu: 500m
            memory: 256Mi
```

**service.yaml:** ClusterIP service on port 80 → container port 8080.

**ingress.yaml:** Ingress with host `example.local` (override via template data).

**hpa.yaml:** HorizontalPodAutoscaler targeting 80% CPU, min 1, max 5 replicas.

## Observability

All services accessible from the host:

| Service | URL | Credentials |
|---------|-----|-------------|
| Grafana | http://localhost:3000 | admin / admin |
| Prometheus | http://localhost:9090 | — |
| Loki | http://localhost:3100 | — |
| Tempo | http://localhost:3200 | — |

Grafana comes pre-provisioned with:
- Prometheus, Loki, and Tempo datasources
- Pipeline monitoring dashboard (stage durations, success/fail rates)
- API health dashboard (latency, error rates)
- Deployment history dashboard

## Learning Layer

The worker runs a learning analyzer every 24h (and on startup) that:
- Analyzes completed jobs for resource waste patterns
- Detects recurring failure stages
- Produces CPU/memory recommendations
- Logs findings to stdout

The analyzer is read-only — recommendations are surfaced via logs and the `GET /api/v1/recommendations` endpoint.

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `DATABASE_URL` | — | Postgres connection string |
| `REDIS_ADDR` | `localhost:6379` | Redis address |
| `PORT` | `8080` | API server port |
| `WORKER_CONCURRENCY` | `4` | Parallel task processing |
| `POLICY_PATH` | `policies/abac/authz.rego` | OPA policy file |
| `CONTAINER_REGISTRY` | `registry:5000` | Internal registry host:port |
| `OTLP_ENDPOINT` | — | OpenTelemetry gRPC endpoint |
| `DEEPSEEK_API_KEY` | — | AI manifest review (optional) |
| `WEBHOOK_URL` | — | POST job results on completion (optional) |

## Assumptions & Limitations

1. **Docker Desktop required.** The compose stack relies on Docker's built-in networking (`host.docker.internal` is not used). Works on macOS with Docker Desktop 4.30+.

2. **ARM64 images.** The Dockerfile and verification binaries (kubeconform, trivy) are pinned to ARM64 (Apple Silicon). For AMD64/x86_64, change the download URLs in `docker/Dockerfile` to the `amd64` variants.

3. **k3s single-node.** The embedded k3s cluster runs as a single control-plane node. No HA, no multi-node scheduling. Adequate for dev/CI.

4. **Registry is HTTP-only.** The internal registry (`registry:5000`) has no TLS. This is configured as insecure in both the dind daemon (`--insecure-registry`) and k3s containerd (`registries.yaml`). Not suitable for production.

5. **No persistent storage for registry images.** Images pushed to the registry live in a Docker volume. If the volume is removed (`docker compose down -v`), all images are lost.

6. **Job workspace is ephemeral.** Cloned repos and generated manifests live in `/tmp/workspaces/` inside the worker container. They're lost on container restart.

7. **Minimal auth.** Token-to-role mapping is hardcoded in `internal/api/middleware.go` (three tokens: `admin-token`, `dev-token`, `view-token`). Intended for dev/CI, not production.

8. **No job cancellation.** The `DELETE /api/v1/jobs/{id}` endpoint is not yet implemented. To cancel a stuck job, update its status directly in Postgres:
   ```sql
   UPDATE jobs SET status='FAILED' WHERE id='<job-uuid>';
   ```

9. **Inline OPA policy.** The ABAC policy at `policies/abac/authz.rego` is loaded from disk at startup. Changes require a restart.

10. **Trivy scans require internet.** On first run, Trivy downloads vulnerability database. Subsequent runs use a cached DB. The scan is skipped if the image isn't accessible from the Docker daemon.

## Sample Demo Log

```
$ docker compose -f docker/docker-compose.yml --env-file docker/.env up -d --build
[+] Building 35.8s (24/24) FINISHED
[+] Running 11/11
 ✔ Container deploy-api-k3s-1         Started
 ✔ Container deploy-api-postgres-1    Started (healthy)
 ✔ Container deploy-api-redis-1       Started
 ✔ Container deploy-api-registry-1    Started
 ✔ Container deploy-api-api-1         Started (healthy)
 ✔ Container deploy-api-worker-1      Started
 ✔ Container deploy-api-loki-1        Started
 ✔ Container deploy-api-promtail-1    Started
 ✔ Container deploy-api-tempo-1       Started
 ✔ Container deploy-api-prometheus-1  Started
 ✔ Container deploy-api-grafana-1     Started

$ curl http://localhost:8080/healthz
{"status":"ok"}

$ curl -s -X POST http://localhost:8080/api/v1/jobs \
  -H "Authorization: Bearer admin-token" \
  -H "Content-Type: application/json" \
  -d @scripts/sample-job.json
{"id":"abc-123","status":"QUEUED"}

# 30 seconds later...
$ curl -s http://localhost:8080/api/v1/jobs/abc-123 \
  -H "Authorization: Bearer admin-token" | python3 -m json.tool
{
    "id": "abc-123",
    "status": "COMPLETED",
    "current_stage": "HEALTH_CHECKING",
    "stage_timestamps": {
        "CLONING": "2026-06-07T03:09:38+05:30",
        "BUILDING": "2026-06-07T03:09:39+05:30",
        "PUSHING": "2026-06-07T03:09:42+05:30",
        "MANIFEST_GENERATING": "2026-06-07T03:09:42+05:30",
        "VERIFYING": "2026-06-07T03:09:44+05:30",
        "APPLYING": "2026-06-07T03:10:01+05:30",
        "HEALTH_CHECKING": "2026-06-07T03:10:01+05:30"
    }
}

$ docker exec deploy-api-k3s-1 kubectl get pods -n sandbox
NAME                          READY   STATUS    RESTARTS   AGE
app-abc-123-67cdf97dcb-qpwd4  1/1     Running   0          32s
```
