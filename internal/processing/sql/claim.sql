WITH candidate AS (
 SELECT id FROM processing_jobs WHERE
 ((status='queued' AND available_at<=NOW()) OR (status='running' AND lease_until<=NOW())) AND attempts<5
 AND NOT EXISTS(SELECT 1 FROM processing_jobs active WHERE active.worker_id=$1 AND active.instance_id=$2
 AND active.status='running' AND active.lease_until>NOW())
 ORDER BY created_at FOR UPDATE SKIP LOCKED LIMIT 1
)
UPDATE processing_jobs j SET status='running',worker_id=$1,instance_id=$2,lease_id=$3,
 lease_until=NOW()+INTERVAL '90 seconds',attempts=attempts+1 FROM candidate
WHERE j.id=candidate.id RETURNING j.id,j.payload;
