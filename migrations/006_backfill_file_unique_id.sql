-- Legacy photo items were named "photo_<file_unique_id>.jpg" before we stored
-- the id explicitly. Recover it so re-sending the same photo overwrites them
-- (including records that previously failed AI description).
UPDATE items
SET file_unique_id = regexp_replace(file_name, '^photo_(.+)\.jpg$', '\1')
WHERE type = 'image'
  AND file_unique_id IS NULL
  AND file_name ~ '^photo_.+\.jpg$';
