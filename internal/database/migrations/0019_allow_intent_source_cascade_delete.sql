-- A Project delete cascades through Intent Sources after the parent Project
-- row is no longer visible. Keep direct Source deletion guarded while allowing
-- that referential cleanup to complete.
DROP TRIGGER intent_sources_delete_immutable;

CREATE TRIGGER intent_sources_delete_immutable
BEFORE DELETE ON intent_sources
WHEN COALESCE((SELECT deleting FROM projects WHERE id = OLD.project_id), 0) = 0
  AND EXISTS (SELECT 1 FROM projects WHERE id = OLD.project_id)
BEGIN
    SELECT RAISE(ABORT, 'intent sources are immutable');
END;
