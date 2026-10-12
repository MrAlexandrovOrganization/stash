INSERT INTO processing_jobs(id,payload) VALUES($1,$2)
ON CONFLICT(id) DO UPDATE SET accessed_at=NOW() RETURNING id;
