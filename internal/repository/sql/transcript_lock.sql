SELECT request_id, item_id, generation, storage_path, audio_format, job_id, state
FROM transcription_tasks WHERE request_id = $1 AND job_id = $2 FOR UPDATE;
