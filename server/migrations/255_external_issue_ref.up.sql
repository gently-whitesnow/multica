-- Immutable identity binding between an external task and its Multica issue.
-- Relationships are application-enforced; no foreign keys or cascades.
CREATE TABLE external_issue_ref (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL,
    provider TEXT NOT NULL CHECK (length(provider) BETWEEN 1 AND 64),
    instance_id TEXT NOT NULL CHECK (length(instance_id) BETWEEN 1 AND 255),
    external_id TEXT NOT NULL CHECK (length(external_id) BETWEEN 1 AND 255),
    issue_id UUID NOT NULL,
    payload_hash BYTEA NOT NULL CHECK (octet_length(payload_hash) = 32),
    external_url TEXT,
    created_by_service_principal_id UUID NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
