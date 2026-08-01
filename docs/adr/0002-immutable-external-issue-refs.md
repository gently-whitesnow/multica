# ADR-0002: Immutable external issue references

## Status

Accepted.

## Context

An external system may retry after a timeout or race multiple workers while
creating the Multica issue that represents one external task. Title, body,
metadata, and URLs are mutable presentation data, so searching any of them
cannot establish identity or prevent duplicate execution.

## Decision

An external issue is identified only by
`(workspace_id, provider, instance_id, external_id)`. The tuple binds exactly
one immutable `issue_id`; one issue can have at most one external reference.
The binding and issue row commit in the same PostgreSQL transaction.

Creation takes a transaction-scoped advisory lock over the tuple. If no
binding exists, Multica creates the issue and reference atomically. If it does
exist, Multica compares a SHA-256 digest of the normalized creation payload:
an equal digest returns the original issue, while a different digest returns
`409 Conflict` without mutating it. The digest is never returned by the API.

The identity tuple does not include the title, description, status, priority,
assignee, project, external URL, metadata, or any external snapshot. A terminal
issue remains terminal on retry. Starting another execution requires a new
external ID. Externally referenced issues cannot be deleted through the issue
API because deletion would make a later retry impossible to resolve; workspace
deletion removes references as part of its explicit teardown transaction.

Only a service principal with `projections:create` may create a binding, and
only one with `projections:read` may resolve it. The authenticated principal's
workspace is authoritative; callers cannot select a workspace in the payload.
The issue creator is recorded as `service_principal`, preserving machine
attribution without impersonating the credential owner.

Projection creation may record an eligible assignee but never enqueues an agent
run. Service-principal projection scopes are not execution authority; a
separate authorized lifecycle action must start work.

## Consequences

The API is intentionally a separate integration surface rather than an option
on ordinary member issue creation. Callers must persist stable provider,
instance, and external IDs. A changed desired payload is an explicit conflict,
not an update operation. Mutable synchronization, if needed, belongs to a
separate scope and endpoint.

External backlinks accept only HTTP(S) URLs without userinfo or query
parameters. This keeps credentials and signed query strings out of issue API
responses and logs; secrets still belong in the integration's secret store.
