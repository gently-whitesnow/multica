-- name: CreateServicePrincipal :one
INSERT INTO service_principal (
    workspace_id, owner_user_id, created_by_user_id, name, scopes, token_hash, token_prefix
) VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: LockServicePrincipalOwner :exec
SELECT pg_advisory_xact_lock(hashtextextended(
    sqlc.arg(workspace_id)::uuid::text || ':' ||
    sqlc.arg(owner_user_id)::uuid::text || ':service-principal-owner', 0
));

-- name: ListServicePrincipalsByWorkspace :many
SELECT * FROM service_principal
WHERE workspace_id = $1
ORDER BY created_at DESC;

-- name: GetServicePrincipalByTokenHash :one
SELECT * FROM service_principal
WHERE token_hash = $1 AND status = 'active';

-- name: RotateServicePrincipalCredential :one
UPDATE service_principal
SET token_hash = $3,
    token_prefix = $4,
    credential_version = credential_version + 1,
    updated_at = now(),
    last_used_at = NULL
WHERE id = $1 AND workspace_id = $2 AND status = 'active'
RETURNING *;

-- name: RevokeServicePrincipal :one
UPDATE service_principal
SET status = 'revoked', revoked_at = now(), updated_at = now()
WHERE id = $1 AND workspace_id = $2 AND status = 'active'
RETURNING *;

-- name: RevokeServicePrincipalsByOwner :many
UPDATE service_principal
SET status = 'revoked', revoked_at = now(), updated_at = now()
WHERE workspace_id = $1 AND owner_user_id = $2 AND status = 'active'
RETURNING *;

-- name: UpdateServicePrincipalLastUsed :exec
UPDATE service_principal SET last_used_at = now() WHERE id = $1 AND status = 'active';

-- name: CreateServicePrincipalAudit :exec
INSERT INTO service_principal_audit (
    workspace_id, service_principal_id, actor_type, actor_id, owner_user_id, action, details
) VALUES ($1, $2, $3, $4, $5, $6, $7);
