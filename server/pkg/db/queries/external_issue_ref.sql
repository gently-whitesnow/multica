-- name: LockExternalIssueRefKey :exec
SELECT pg_advisory_xact_lock(hashtextextended(
    sqlc.arg(workspace_id)::uuid::text || ':' || sqlc.arg(provider)::text || ':' ||
    sqlc.arg(instance_id)::text || ':' || sqlc.arg(external_id)::text || ':external-issue-ref', 0
));

-- name: GetExternalIssueRef :one
SELECT * FROM external_issue_ref
WHERE workspace_id = $1 AND provider = $2 AND instance_id = $3 AND external_id = $4;

-- name: GetExternalIssueRefByIssue :one
SELECT * FROM external_issue_ref
WHERE workspace_id = $1 AND issue_id = $2;

-- name: CreateExternalIssueRef :one
INSERT INTO external_issue_ref (
    workspace_id, provider, instance_id, external_id, issue_id, payload_hash,
    external_url, created_by_service_principal_id
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING *;
