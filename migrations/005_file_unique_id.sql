ALTER TABLE items ADD COLUMN IF NOT EXISTS file_unique_id TEXT;

-- Partial unique index: enforces at most one item per Telegram file while
-- still allowing many items with no file_unique_id (e.g. documents).
CREATE UNIQUE INDEX IF NOT EXISTS items_file_unique_id_idx
    ON items (file_unique_id) WHERE file_unique_id IS NOT NULL;
