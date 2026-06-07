# =============================================================================
# Build stage — shared: compiles both api and worker binaries
# =============================================================================
FROM golang:1.26-alpine AS builder
RUN apk add --no-cache git ca-certificates
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /app/api ./cmd/api && \
    CGO_ENABLED=0 GOOS=linux go build -o /app/worker ./cmd/worker

# =============================================================================
# api-runtime — minimal Alpine image for the HTTP API server
# =============================================================================
FROM alpine:3.20 AS api-runtime
RUN apk add --no-cache ca-certificates tzdata curl && \
    cp /usr/share/zoneinfo/Asia/Kolkata /etc/localtime && \
    echo "Asia/Kolkata" > /etc/timezone
ENV TZ=Asia/Kolkata
COPY --from=builder /app/api /usr/local/bin/api
COPY policies/ /etc/deploy/policies/
EXPOSE 8080
CMD ["/usr/local/bin/api"]

# =============================================================================
# worker-runtime — Docker-in-Docker image for the deploy worker
# =============================================================================
FROM docker:24-dind AS worker-runtime
RUN apk add --no-cache ca-certificates tzdata curl && \
    cp /usr/share/zoneinfo/Asia/Kolkata /etc/localtime && \
    echo "Asia/Kolkata" > /etc/timezone
ENV TZ=Asia/Kolkata

# Verification tools
RUN curl -sL -o /tmp/kubeconform.tar.gz https://github.com/yannh/kubeconform/releases/download/v0.6.7/kubeconform-linux-arm64.tar.gz && \
    tar -xzf /tmp/kubeconform.tar.gz -C /usr/local/bin/ kubeconform && \
    rm /tmp/kubeconform.tar.gz
RUN curl -sL -o /tmp/trivy.tar.gz https://github.com/aquasecurity/trivy/releases/download/v0.71.0/trivy_0.71.0_Linux-ARM64.tar.gz && \
    tar -xzf /tmp/trivy.tar.gz -C /usr/local/bin/ trivy && \
    rm /tmp/trivy.tar.gz

COPY --from=builder /app/worker /usr/local/bin/worker
COPY policies/ /etc/deploy/policies/
COPY docker/worker-entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

ENTRYPOINT ["/entrypoint.sh"]
