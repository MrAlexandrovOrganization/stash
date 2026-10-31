UPDATE transcription_tasks SET state = 'ack', terminal_status = $3, result_text = $4,
    attempts = 0, next_attempt_at = NOW()
WHERE request_id = $1 AND job_id = $2 AND state = 'pending';
