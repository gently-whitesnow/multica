DELETE FROM issue
WHERE creator_type = 'service_principal'
  AND id IN (SELECT issue_id FROM external_issue_ref);

ALTER TABLE issue DROP CONSTRAINT IF EXISTS issue_creator_type_check;
ALTER TABLE issue ADD CONSTRAINT issue_creator_type_check
    CHECK (creator_type IN ('member', 'agent'));
