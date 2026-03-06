ALTER TABLE integrations ADD COLUMN attachments TEXT NOT NULL DEFAULT '[]';

-- Migrate existing integration_attachments to the new column.
-- For each integration, collect attached box names as JSON array of "vm:name" entries.
UPDATE integrations SET attachments = (
    SELECT COALESCE(
        json_group_array('vm:' || b.name),
        '[]'
    )
    FROM integration_attachments ia
    JOIN boxes b ON ia.box_id = b.id
    WHERE ia.integration_id = integrations.integration_id
)
WHERE EXISTS (
    SELECT 1 FROM integration_attachments ia WHERE ia.integration_id = integrations.integration_id
);

DROP INDEX IF EXISTS idx_integration_attachments_box;
DROP TABLE IF EXISTS integration_attachments;
