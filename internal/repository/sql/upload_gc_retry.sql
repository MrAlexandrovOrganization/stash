UPDATE file_gc SET not_before = NOW() + INTERVAL '1 minute'
WHERE storage_path = $1;
