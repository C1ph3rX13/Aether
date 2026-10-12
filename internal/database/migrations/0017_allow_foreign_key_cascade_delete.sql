-- Foreign-key cascades run after the parent row is no longer visible to a
-- child DELETE trigger. Keep direct child deletion guarded while allowing the
-- Project hard-delete cascade to remove owned Blackboard rows.
DROP TRIGGER facts_delete_only_with_project;
CREATE TRIGGER facts_delete_only_with_project
BEFORE DELETE ON facts
WHEN COALESCE((SELECT deleting FROM projects WHERE id = OLD.project_id), 0) = 0
 AND EXISTS (SELECT 1 FROM projects WHERE id = OLD.project_id)
BEGIN
    SELECT RAISE(ABORT, 'facts can only be deleted with their project');
END;

DROP TRIGGER intents_delete_only_with_project;
CREATE TRIGGER intents_delete_only_with_project
BEFORE DELETE ON intents
WHEN COALESCE((SELECT deleting FROM projects WHERE id = OLD.project_id), 0) = 0
 AND EXISTS (SELECT 1 FROM projects WHERE id = OLD.project_id)
BEGIN
    SELECT RAISE(ABORT, 'intents can only be deleted with their project');
END;

DROP TRIGGER hints_delete_only_with_project;
CREATE TRIGGER hints_delete_only_with_project
BEFORE DELETE ON hints
WHEN COALESCE((SELECT deleting FROM projects WHERE id = OLD.project_id), 0) = 0
 AND EXISTS (SELECT 1 FROM projects WHERE id = OLD.project_id)
BEGIN
    SELECT RAISE(ABORT, 'hints can only be deleted with their project');
END;
