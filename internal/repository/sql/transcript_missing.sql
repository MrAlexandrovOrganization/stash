WITH reset AS (
    UPDATE transcription_tasks SET job_id = NULL, attempts = 0, next_attempt_at = NOW()
    WHERE request_id = $1 AND job_id = $2 AND state = 'pending'
    RETURNING *
)
UPDATE items i SET transcript_job_id = NULL FROM reset s
WHERE i.id = s.item_id AND i.transcript_generation = s.generation
AND i.storage_path = s.storage_path AND i.transcript_job_id = $2;
