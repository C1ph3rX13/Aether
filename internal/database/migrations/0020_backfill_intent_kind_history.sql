-- History snapshots predate the persisted Intent Kind column. Backfill the
-- serialized ProjectDetail images so Web and History readers see the same
-- business discriminator as the live Blackboard rows.
UPDATE blackboard_changes
SET data = json_set(data, '$.after.intents', (
    SELECT json_group_array(json_set(value, '$.kind', CASE
        WHEN json_extract(value, '$.kind') IN ('bootstrap', 'explore', 'completion')
            THEN json_extract(value, '$.kind')
        WHEN (SELECT live.kind
              FROM intents live
              WHERE live.project_id = blackboard_changes.project_id
                AND live.id = json_extract(value, '$.id')) IN ('bootstrap', 'explore', 'completion')
            THEN (SELECT live.kind
                  FROM intents live
                  WHERE live.project_id = blackboard_changes.project_id
                    AND live.id = json_extract(value, '$.id'))
        WHEN json_extract(value, '$.to') = 'goal'
            THEN 'completion'
        WHEN json_extract(value, '$.description') = 'bootstrap'
         AND json_extract(value, '$.creator') = 'dispatcher.bootstrap'
         AND json_array_length(json_extract(value, '$.from')) = 1
         AND json_extract(value, '$.from[0]') = 'origin'
            THEN 'bootstrap'
        ELSE 'explore'
    END))
    FROM (
        SELECT key, value
        FROM json_each(CASE WHEN json_valid(data)
                            THEN json_extract(data, '$.after.intents')
                            ELSE '[]' END)
        ORDER BY CAST(key AS INTEGER)
    )
))
WHERE CASE WHEN json_valid(data) THEN json_type(data, '$.after.intents') END = 'array'
  AND EXISTS (
      SELECT 1
      FROM json_each(CASE WHEN json_valid(data)
                          THEN json_extract(data, '$.after.intents')
                          ELSE '[]' END)
      WHERE json_type(value, '$.kind') IS NULL
  );

UPDATE blackboard_changes
SET data = json_set(data, '$.before.intents', (
    SELECT json_group_array(json_set(value, '$.kind', CASE
        WHEN json_extract(value, '$.kind') IN ('bootstrap', 'explore', 'completion')
            THEN json_extract(value, '$.kind')
        WHEN (SELECT live.kind
              FROM intents live
              WHERE live.project_id = blackboard_changes.project_id
                AND live.id = json_extract(value, '$.id')) IN ('bootstrap', 'explore', 'completion')
            THEN (SELECT live.kind
                  FROM intents live
                  WHERE live.project_id = blackboard_changes.project_id
                    AND live.id = json_extract(value, '$.id'))
        WHEN json_extract(value, '$.to') = 'goal'
            THEN 'completion'
        WHEN json_extract(value, '$.description') = 'bootstrap'
         AND json_extract(value, '$.creator') = 'dispatcher.bootstrap'
         AND json_array_length(json_extract(value, '$.from')) = 1
         AND json_extract(value, '$.from[0]') = 'origin'
            THEN 'bootstrap'
        ELSE 'explore'
    END))
    FROM (
        SELECT key, value
        FROM json_each(CASE WHEN json_valid(data)
                            THEN json_extract(data, '$.before.intents')
                            ELSE '[]' END)
        ORDER BY CAST(key AS INTEGER)
    )
))
WHERE CASE WHEN json_valid(data) THEN json_type(data, '$.before.intents') END = 'array'
  AND EXISTS (
      SELECT 1
      FROM json_each(CASE WHEN json_valid(data)
                          THEN json_extract(data, '$.before.intents')
                          ELSE '[]' END)
      WHERE json_type(value, '$.kind') IS NULL
  );
