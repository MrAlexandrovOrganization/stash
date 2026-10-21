-- A legacy database contained two heap rows for one Telegram ID despite its
-- unique index reporting valid. Keep every item, original identifier, timestamp,
-- vector and object path. Only the newest-created copy owns future deduplication;
-- other copies remain independently readable/editable/deletable by item ID.
-- Run under the migration transaction/advisory lock, blocking concurrent writes.
LOCK TABLE items IN ACCESS EXCLUSIVE MODE;
DROP INDEX items_file_unique_id_idx;

ALTER TABLE items
    ADD COLUMN file_unique_id_legacy_duplicate BOOLEAN NOT NULL DEFAULT FALSE;

-- The damaged index has already been dropped: the ranking must see every heap
-- row. ID breaks ties deterministically. Changing only this marker does not fire
-- the source-content/processing-generation triggers.
WITH ranked AS (
    SELECT id, row_number() OVER (
        PARTITION BY file_unique_id ORDER BY created_at DESC, id DESC
    ) AS position
    FROM items
    WHERE file_unique_id IS NOT NULL
)
UPDATE items SET file_unique_id_legacy_duplicate = TRUE
FROM ranked WHERE items.id = ranked.id AND ranked.position > 1;

CREATE UNIQUE INDEX items_file_unique_id_idx ON items (file_unique_id)
    WHERE file_unique_id IS NOT NULL AND NOT file_unique_id_legacy_duplicate;

COMMENT ON COLUMN items.file_unique_id_legacy_duplicate IS
    'Preserved historical duplicate; excluded from Telegram re-upload deduplication. Original file_unique_id and media remain intact.';
