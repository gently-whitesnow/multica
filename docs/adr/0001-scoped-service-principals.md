# ADR-0001: Scoped service principals

## Status

Accepted.

## Context

External integrations need durable API credentials, but a human PAT would make
the integration indistinguishable from its owner and inherit every member API.
Agent and daemon credentials are also unsuitable: they can start work and are
bound to runtime lifecycle rather than an integration.

## Decision

A service principal is a workspace-bound, non-assignable machine identity. Its
credential uses the `msp_` prefix and resolves to three separate identities:
the principal actor, its workspace, and the accountable human owner. The owner
is recorded for audit only and is never used to grant member authorization.

The existing member/agent route tree rejects service principals. Integration
routes must live in a separate route group and explicitly attach exactly one of
the following scopes:

- `projections:create`
- `projections:read`
- `projections:update`
- `approvals:read`
- `approvals:decide`

Missing and unknown scopes fail closed. Only a human workspace owner or admin
may create, rotate, list, or revoke a principal. Scopes cannot be edited or
self-expanded; changing them requires revocation and a new principal.

Only hashes are stored. The plaintext credential is returned once after create
or rotate and is absent from list, identity, audit, logs, and issue data.
Rotation atomically replaces the hash, so the previous credential stops working
immediately. Revocation is also checked directly against PostgreSQL, without an
authentication cache.

## Threat model

The design assumes a credential can be copied from the external system. Its
blast radius is therefore the intersection of one workspace, its fixed scopes,
and the purpose-built integration endpoints that opt into those scopes. A
credential cannot use member APIs, be an assignee or approver, start an agent or
shell, read repository/runtime secrets, impersonate a member, or mint broader
scopes. Client-supplied actor and scope headers are stripped by authentication.

Request logs name `actor_type=service_principal`, the principal actor ID, and
the accountable owner ID. Lifecycle audit rows preserve the same distinction
without storing credential material.

## Consequences

Projection and approval resources are deliberately out of scope here. Their
handlers must use the fixed scopes above and must never reuse workspace member
middleware as authorization for a service principal.
