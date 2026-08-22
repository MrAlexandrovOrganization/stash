ALTER TABLE items ADD COLUMN IF NOT EXISTS ai_description_error TEXT;

CREATE INDEX IF NOT EXISTS items_ai_description_error_idx
    ON items (ai_description_error) WHERE ai_description_error IS NOT NULL;
