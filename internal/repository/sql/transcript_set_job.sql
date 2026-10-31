WITH saved AS (
    UPDATE transcription_tasks SET job_id = $2, attempts = 0, next_attempt_at = NOW()
    WHERE request_id = $1 AND state = 'pending' AND (job_id IS NULL OR job_id = $2)
    RETURNING *
)
UPDATE items i SET transcript_job_id = s.job_id, updated_at = NOW()
FROM saved s WHERE i.id = s.item_id AND i.transcript_generation = s.generation
AND i.storage_path = s.storage_path AND i.transcript IS NULL;
