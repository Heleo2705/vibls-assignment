#!/bin/sh
set -e

# ---------------------------------------------------------------------------
# 1. Start Docker daemon (background)
# ---------------------------------------------------------------------------
dockerd --insecure-registry registry:5000 > /var/log/dockerd.log 2>&1 &

for i in $(seq 1 30); do
    if docker info >/dev/null 2>&1; then
        echo "Docker daemon ready"
        break
    fi
    sleep 1
done
if ! docker info >/dev/null 2>&1; then
    echo "ERROR: Docker daemon failed to start" >&2
    cat /var/log/dockerd.log
    exit 1
fi

# ---------------------------------------------------------------------------
# 2. Generate kubeconfig for k3s
# ---------------------------------------------------------------------------
# Copy k3s kubeconfig from the shared volume, replacing the server address
# from 127.0.0.1 to k3s (compose service name).
mkdir -p /root/.kube
if [ -f /kubeconfig/k3s.yaml ]; then
    sed 's/127\.0\.0\.1:6443/k3s:6443/g' /kubeconfig/k3s.yaml > /root/.kube/config
    echo "Kubeconfig ready (k3s:6443)"
elif [ -f /kubeconfig/yaml ]; then
    sed 's/127\.0\.0\.1:6443/k3s:6443/g' /kubeconfig/yaml > /root/.kube/config
    echo "Kubeconfig ready (k3s:6443)"
elif [ -f /var/lib/rancher/k3s/server/token ]; then
    # Fallback: generate a simple kubeconfig with insecure TLS
    # This works when we don't have client certs
    KTOKEN=$(cat /var/lib/rancher/k3s/server/token 2>/dev/null | head -1)
    cat > /root/.kube/config <<EOF
apiVersion: v1
clusters:
- cluster:
    server: https://k3s:6443
    insecure-skip-tls-verify: true
  name: k3s
users:
- name: admin
  user:
    token: ${KTOKEN}
contexts:
- context:
    cluster: k3s
    user: admin
  name: k3s
current-context: k3s
EOF
    echo "Kubeconfig generated with token (k3s:6443)"
else
    echo "WARNING: No kubeconfig found at /kubeconfig/k3s.yaml. Apply stage will fail." >&2
fi

# ---------------------------------------------------------------------------
# 3. Run worker
# ---------------------------------------------------------------------------
echo "Worker starting..."
exec /usr/local/bin/worker
