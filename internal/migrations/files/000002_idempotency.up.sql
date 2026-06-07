CREATE TABLE IF NOT EXISTS idempotency_keys (
    hash        TEXT PRIMARY KEY,
    job_id      UUID NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
    response    JSONB NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_idempotency_keys_created
    ON idempotency_keys(created_at);

CREATE UNIQUE INDEX IF NOT EXISTS idx_jobs_one_active_deploy
    ON jobs(repo_url, branch, target_namespace)
    WHERE status IN ('QUEUED', 'RUNNING');
