-- Private MCP application credentials and command admission.
-- The raw token is never stored. payload arguments are not stored.

CREATE TABLE IF NOT EXISTS mcp_credentials (
    id TEXT PRIMARY KEY,
    owner TEXT NOT NULL,
    name TEXT NOT NULL,
    token_hash TEXT NOT NULL UNIQUE,
    scopes TEXT[] NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    revoked_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_used_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS mcp_credentials_owner_created_idx
    ON mcp_credentials (owner, created_at DESC);

CREATE TABLE IF NOT EXISTS mcp_invocations (
    request_id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    credential_id TEXT NOT NULL,
    tool TEXT NOT NULL,
    tool_version TEXT NOT NULL,
    catalog_version TEXT NOT NULL,
    payload_hash TEXT NOT NULL,
    idempotency_key TEXT NOT NULL,
    inference_allowed BOOLEAN NOT NULL,
    outcome TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS mcp_invocations_owner_time_idx
    ON mcp_invocations (user_id, created_at DESC);

-- Per-owner MCP request ledger. No arguments, tokens, or raw credentials.
-- inference_units is the internal model count for that request. Research calls store 0.

CREATE TABLE IF NOT EXISTS mcp_usage (
    id BIGSERIAL PRIMARY KEY,
    user_id TEXT NOT NULL,
    credential_id TEXT NOT NULL,
    tool TEXT NOT NULL,
    outcome TEXT NOT NULL,
    duration_ms BIGINT NOT NULL,
    inference_units BIGINT NOT NULL,
    job_id TEXT NOT NULL DEFAULT '',
    detail TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT mcp_usage_duration_nonnegative CHECK (duration_ms >= 0),
    CONSTRAINT mcp_usage_inference_nonnegative CHECK (inference_units >= 0)
);

CREATE INDEX IF NOT EXISTS mcp_usage_owner_time_idx
    ON mcp_usage (user_id, created_at DESC);

CREATE INDEX IF NOT EXISTS mcp_usage_credential_time_idx
    ON mcp_usage (credential_id, created_at DESC);

ALTER TABLE mcp_usage ADD COLUMN IF NOT EXISTS detail TEXT NOT NULL DEFAULT '';
