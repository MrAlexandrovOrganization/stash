INSERT INTO file_gc (storage_path) VALUES ($1)
ON CONFLICT (storage_path) DO NOTHING;
