UPDATE transcription_tasks SET state = 'done', result_text = NULL, attempts = 0
WHERE request_id = $1 AND job_id = $2 AND state = 'ack';
