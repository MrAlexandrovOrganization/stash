SELECT worker_id,COUNT(*),COALESCE(MAX(EXTRACT(EPOCH FROM NOW()-attempt_started_at)),0) FROM processing_jobs WHERE status='running' AND lease_until>NOW() GROUP BY worker_id;
