WITH current AS (
    SELECT id, transcript_generation, storage_path, transcript_job_id
    FROM items WHERE id = $1 AND storage_path = $2 AND type = 'video' AND transcript IS NULL
    FOR UPDATE
)
INSERT INTO transcription_tasks (request_id, item_id, generation, storage_path, audio_format, job_id)
SELECT $3, id, transcript_generation, storage_path, $4, transcript_job_id FROM current
ON CONFLICT (item_id, generation) DO NOTHING;
