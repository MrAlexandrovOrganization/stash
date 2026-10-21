SELECT id, type, file_name, content_type, size, storage_path, description, tags,
    source, original_caption, transcript, ai_description, ai_description_error,
    transcript_job_id, telegram_file_id, file_unique_id, created_at, updated_at,
    processing_generation
FROM items
WHERE file_unique_id = $1 AND NOT file_unique_id_legacy_duplicate;
