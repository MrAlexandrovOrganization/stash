ALTER TABLE items ADD COLUMN IF NOT EXISTS source TEXT NOT NULL DEFAULT '';
ALTER TABLE items ADD COLUMN IF NOT EXISTS original_caption TEXT NOT NULL DEFAULT '';
ALTER TABLE items ADD COLUMN IF NOT EXISTS ai_description TEXT;

CREATE INDEX IF NOT EXISTS items_original_caption_trgm_idx
    ON items USING GIN(original_caption gin_trgm_ops);

CREATE INDEX IF NOT EXISTS items_ai_description_trgm_idx
    ON items USING GIN(ai_description gin_trgm_ops);
