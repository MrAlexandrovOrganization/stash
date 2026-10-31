UPDATE items SET transcript = $4, transcript_job_id = NULL, updated_at = NOW()
WHERE id = $1 AND transcript_generation = $2 AND storage_path = $3
AND transcript IS NULL AND transcript_job_id = $5;
