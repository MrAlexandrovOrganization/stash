INSERT INTO file_gc (storage_path, upload_expires_at)
VALUES ($1, NOW() + INTERVAL '1 hour');
