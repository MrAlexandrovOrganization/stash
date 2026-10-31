SELECT storage_path FROM file_gc
WHERE storage_path = $1 AND upload_expires_at > NOW()
FOR UPDATE;
