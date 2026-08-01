-- Workspace-bound machine identities for external integrations. Relationships
-- are enforced in handlers; no foreign keys or cascades are used.
CREATE TABLE service_principal (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL,
    owner_user_id UUID NOT NULL,
    created_by_user_id UUID NOT NULL,
    name TEXT NOT NULL CHECK (length(btrim(name)) BETWEEN 1 AND 100),
    scopes TEXT[] NOT NULL CHECK (cardinality(scopes) > 0),
    token_hash TEXT NOT NULL,
    token_prefix TEXT NOT NULL,
    credential_version INTEGER NOT NULL DEFAULT 1 CHECK (credential_version > 0),
    status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'revoked')),
    last_used_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    revoked_at TIMESTAMPTZ
);

CREATE TABLE service_principal_audit (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL,
    service_principal_id UUID NOT NULL,
    actor_type TEXT NOT NULL CHECK (actor_type IN ('member', 'service_principal')),
    actor_id UUID NOT NULL,
    owner_user_id UUID NOT NULL,
    action TEXT NOT NULL CHECK (action IN ('created', 'rotated', 'revoked')),
    details JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
