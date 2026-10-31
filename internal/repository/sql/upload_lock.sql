SELECT storage_path FROM file_gc
WHERE storage_path = $1 AND not_before > NOW()
FOR UPDATE;
