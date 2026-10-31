UPDATE transcription_tasks SET attempts = attempts + 1,
    next_attempt_at = NOW() + make_interval(secs => LEAST(300, 5 * (1 << LEAST(attempts, 6))))
WHERE request_id = $1 AND state <> 'done';
