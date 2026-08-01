CREATE UNIQUE INDEX CONCURRENTLY external_issue_ref_key_uidx ON external_issue_ref (workspace_id, provider, instance_id, external_id);
