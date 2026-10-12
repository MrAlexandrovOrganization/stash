SELECT COALESCE(MAX(EXTRACT(EPOCH FROM NOW()-created_at)),0) FROM processing_jobs WHERE status='queued' OR (status='running' AND lease_until<=NOW());
