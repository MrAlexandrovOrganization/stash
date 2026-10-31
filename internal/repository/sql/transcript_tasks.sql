SELECT request_id, item_id, generation, storage_path, audio_format, job_id, state
FROM transcription_tasks
WHERE state = $1 AND next_attempt_at <= NOW()
AND ($1 = 'ack' OR (job_id IS NULL) = $2)
ORDER BY next_attempt_at, created_at LIMIT $3;
