DROP TABLE settings;

ALTER TABLE projects DROP COLUMN reason_worker;
ALTER TABLE projects DROP COLUMN reason_trigger;
ALTER TABLE projects DROP COLUMN reason_started_at;
ALTER TABLE projects DROP COLUMN reason_last_heartbeat_at;

ALTER TABLE intents DROP COLUMN worker;
ALTER TABLE intents DROP COLUMN last_heartbeat_at;
