SELECT id, type, file_name, content_type, size, storage_path, description, tags,
    source, original_caption, transcript, ai_description, ai_description_error,
    transcript_job_id, telegram_file_id, file_unique_id, created_at, updated_at, processing_generation
FROM items i WHERE type = 'video' AND transcript IS NULL
AND NOT EXISTS (SELECT FROM transcription_tasks t WHERE t.item_id = i.id AND t.generation = i.transcript_generation)
ORDER BY created_at LIMIT $1;
