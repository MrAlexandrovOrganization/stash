INSERT INTO items (id, type, file_name, content_type, size, storage_path,
    description, tags, source, original_caption, file_unique_id, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
ON CONFLICT (id) DO UPDATE SET
    type = EXCLUDED.type, file_name = EXCLUDED.file_name,
    content_type = EXCLUDED.content_type, size = EXCLUDED.size,
    storage_path = EXCLUDED.storage_path, description = EXCLUDED.description,
    tags = EXCLUDED.tags, source = EXCLUDED.source,
    original_caption = EXCLUDED.original_caption,
    transcript = NULL, transcript_job_id = NULL, telegram_file_id = NULL,
    updated_at = EXCLUDED.updated_at
RETURNING id, type, file_name, content_type, size, storage_path, description, tags,
    source, original_caption, transcript, ai_description, ai_description_error,
    transcript_job_id, telegram_file_id, file_unique_id, created_at, updated_at,
    processing_generation;
