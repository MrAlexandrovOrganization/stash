SELECT id, storage_path, created_at FROM items
WHERE file_unique_id = $1 AND NOT file_unique_id_legacy_duplicate
FOR UPDATE;
