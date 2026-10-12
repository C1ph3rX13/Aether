ALTER TABLE projects ADD COLUMN config_revision TEXT REFERENCES configuration_revisions(id);

CREATE INDEX projects_config_revision_idx ON projects(config_revision);
