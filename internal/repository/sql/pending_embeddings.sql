SELECT id, type, file_name, content_type, size, storage_path, description, tags, source, original_caption, transcript, ai_description, ai_description_error, transcript_job_id, telegram_file_id, file_unique_id, created_at, updated_at, processing_generation FROM items
WHERE type IN ('image','gif','video')
AND (embedding_image IS NULL OR (embedding_text IS NULL AND
 (COALESCE(description,'')<>'' OR COALESCE(original_caption,'')<>'' OR COALESCE(ai_description,'')<>'')))
ORDER BY created_at DESC LIMIT $1;
