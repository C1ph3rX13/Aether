-- Intent kind is a business discriminator, not a value inferred from mutable
-- presentation fields. Existing rows receive a deterministic forward-only
-- classification before new writes are required to provide the column.
ALTER TABLE intents ADD COLUMN kind TEXT NOT NULL DEFAULT 'explore'
    CHECK (kind IN ('bootstrap', 'explore', 'completion'));

UPDATE intents
SET kind = 'completion'
WHERE to_fact_id = 'goal';

UPDATE intents
SET kind = 'bootstrap'
WHERE kind = 'explore'
  AND description = 'bootstrap'
  AND creator = 'dispatcher.bootstrap'
  AND to_fact_id IS NULL
  AND cancelled_at IS NULL
  AND sources_locked = 1
  AND (SELECT COUNT(*)
       FROM intents all_intents
       WHERE all_intents.project_id = intents.project_id) = 1
  AND (SELECT COUNT(*)
       FROM facts active_facts
       WHERE active_facts.project_id = intents.project_id
         AND active_facts.retracted_at IS NULL) = 2
  AND EXISTS (
      SELECT 1 FROM facts origin_fact
      WHERE origin_fact.project_id = intents.project_id
        AND origin_fact.id = 'origin'
        AND origin_fact.retracted_at IS NULL
  )
  AND EXISTS (
      SELECT 1 FROM facts goal_fact
      WHERE goal_fact.project_id = intents.project_id
        AND goal_fact.id = 'goal'
        AND goal_fact.retracted_at IS NULL
  )
  AND (SELECT COUNT(*)
       FROM intent_sources sources
       WHERE sources.project_id = intents.project_id
         AND sources.intent_id = intents.id) = 1
  AND EXISTS (
      SELECT 1 FROM intent_sources origin_source
      WHERE origin_source.project_id = intents.project_id
        AND origin_source.intent_id = intents.id
        AND origin_source.fact_id = 'origin'
  );

CREATE TRIGGER intents_kind_immutable
BEFORE UPDATE OF kind ON intents
FOR EACH ROW WHEN NEW.kind IS NOT OLD.kind
BEGIN
    SELECT RAISE(ABORT, 'intent kind is immutable');
END;

-- Project Detail and scheduler reads are Project-range operations. These
-- indexes add the ownership prefix while preserving the existing composite
-- parent keys required by execution-ledger foreign keys.
CREATE INDEX facts_project_order_idx ON facts(project_id, id);
CREATE INDEX facts_project_active_idx ON facts(project_id, id) WHERE retracted_at IS NULL;
CREATE INDEX intents_project_created_idx ON intents(project_id, created_at, id);
CREATE INDEX intents_project_open_kind_idx
    ON intents(project_id, kind, created_at, id)
    WHERE to_fact_id IS NULL AND cancelled_at IS NULL;
CREATE INDEX hints_project_created_idx ON hints(project_id, created_at, id);
CREATE INDEX hints_project_active_idx ON hints(project_id, created_at, id) WHERE withdrawn_at IS NULL;
CREATE INDEX intent_sources_project_order_idx ON intent_sources(project_id, intent_id, fact_id);
