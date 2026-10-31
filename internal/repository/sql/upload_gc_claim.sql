SELECT storage_path FROM file_gc g
WHERE not_before <= NOW()
AND (upload_expires_at IS NULL OR upload_expires_at <= NOW())
AND NOT EXISTS (SELECT FROM items i WHERE i.storage_path = g.storage_path)
AND NOT EXISTS (SELECT FROM transcription_tasks t WHERE t.storage_path = g.storage_path AND t.state = 'pending')
ORDER BY not_before, storage_path
FOR UPDATE OF g SKIP LOCKED LIMIT 1;
