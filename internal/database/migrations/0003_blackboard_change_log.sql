ALTER TABLE facts ADD COLUMN supersedes_fact_id TEXT;
ALTER TABLE facts ADD COLUMN retracted_at TEXT;
ALTER TABLE facts ADD COLUMN retracted_by TEXT;
ALTER TABLE facts ADD COLUMN retraction_reason TEXT;
ALTER TABLE facts ADD COLUMN replacement_fact_id TEXT;

ALTER TABLE intents ADD COLUMN cancelled_at TEXT;
ALTER TABLE intents ADD COLUMN cancelled_by TEXT;
ALTER TABLE intents ADD COLUMN cancellation_reason TEXT;

ALTER TABLE hints ADD COLUMN withdrawn_at TEXT;
ALTER TABLE hints ADD COLUMN withdrawn_by TEXT;
ALTER TABLE hints ADD COLUMN withdrawal_reason TEXT;
ALTER TABLE projects ADD COLUMN deleting INTEGER NOT NULL DEFAULT 0 CHECK (deleting IN (0, 1));
ALTER TABLE intents ADD COLUMN sources_locked INTEGER NOT NULL DEFAULT 1 CHECK (sources_locked IN (0, 1));

CREATE TABLE blackboard_changes (
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    revision INTEGER NOT NULL CHECK (revision > 0),
    generation INTEGER NOT NULL CHECK (generation > 0),
    kind TEXT NOT NULL,
    entity_type TEXT NOT NULL,
    entity_id TEXT,
    data TEXT NOT NULL DEFAULT '{}',
    created_at TEXT NOT NULL,
    PRIMARY KEY (project_id, revision)
);

CREATE INDEX blackboard_changes_created_at_idx ON blackboard_changes(created_at);

CREATE TRIGGER facts_description_immutable
BEFORE UPDATE OF description ON facts
FOR EACH ROW WHEN NEW.description IS NOT OLD.description
BEGIN
    SELECT RAISE(ABORT, 'fact description is immutable');
END;

CREATE TRIGGER intents_content_immutable
BEFORE UPDATE OF description, creator ON intents
FOR EACH ROW WHEN NEW.description IS NOT OLD.description OR NEW.creator IS NOT OLD.creator
BEGIN
    SELECT RAISE(ABORT, 'intent content is immutable');
END;

CREATE TRIGGER intent_sources_immutable
BEFORE UPDATE ON intent_sources
BEGIN
    SELECT RAISE(ABORT, 'intent sources are immutable');
END;

CREATE TRIGGER intent_sources_insert_immutable
BEFORE INSERT ON intent_sources
WHEN COALESCE((SELECT sources_locked FROM intents WHERE id = NEW.intent_id AND project_id = NEW.project_id), 1) = 1
BEGIN
    SELECT RAISE(ABORT, 'intent sources are immutable');
END;

CREATE TRIGGER intent_sources_delete_immutable
BEFORE DELETE ON intent_sources
WHEN COALESCE((SELECT deleting FROM projects WHERE id = OLD.project_id), 0) = 0
BEGIN
    SELECT RAISE(ABORT, 'intent sources are immutable');
END;

CREATE TRIGGER intent_sources_lock_one_way
BEFORE UPDATE OF sources_locked ON intents
FOR EACH ROW WHEN OLD.sources_locked = 1 AND NEW.sources_locked <> 1
BEGIN
    SELECT RAISE(ABORT, 'intent sources lock is immutable');
END;

CREATE TRIGGER hints_content_immutable
BEFORE UPDATE OF content, creator ON hints
FOR EACH ROW WHEN NEW.content IS NOT OLD.content OR NEW.creator IS NOT OLD.creator
BEGIN
    SELECT RAISE(ABORT, 'hint content is immutable');
END;
