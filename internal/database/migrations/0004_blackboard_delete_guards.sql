CREATE TRIGGER facts_delete_only_with_project
BEFORE DELETE ON facts
WHEN COALESCE((SELECT deleting FROM projects WHERE id = OLD.project_id), 0) = 0
BEGIN
    SELECT RAISE(ABORT, 'facts can only be deleted with their project');
END;

CREATE TRIGGER intents_delete_only_with_project
BEFORE DELETE ON intents
WHEN COALESCE((SELECT deleting FROM projects WHERE id = OLD.project_id), 0) = 0
BEGIN
    SELECT RAISE(ABORT, 'intents can only be deleted with their project');
END;

CREATE TRIGGER hints_delete_only_with_project
BEFORE DELETE ON hints
WHEN COALESCE((SELECT deleting FROM projects WHERE id = OLD.project_id), 0) = 0
BEGIN
    SELECT RAISE(ABORT, 'hints can only be deleted with their project');
END;
