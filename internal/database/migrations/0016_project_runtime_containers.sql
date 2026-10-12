CREATE TABLE project_runtime_containers (
    project_id TEXT PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
    container_id TEXT NOT NULL,
    recorded_at TEXT NOT NULL
);

INSERT INTO project_runtime_containers (project_id, container_id, recorded_at)
SELECT handles.project_id, handles.container_id, handles.started_at
FROM execution_runtime_handles AS handles
WHERE NOT EXISTS (
    SELECT 1
    FROM execution_runtime_handles AS newer
    WHERE newer.project_id = handles.project_id
      AND (newer.started_at > handles.started_at OR
           (newer.started_at = handles.started_at AND newer.exec_id > handles.exec_id))
);
