UPDATE processing_jobs SET status=CASE WHEN $5 OR attempts>=5 THEN 'failed' ELSE 'queued' END,
available_at=NOW()+LEAST(300,5*(2^attempts)::int)*INTERVAL '1 second',
payload=CASE WHEN $5 OR attempts>=5 THEN ''::bytea ELSE payload END
WHERE id=$1 AND worker_id=$2 AND instance_id=$3 AND lease_id=$4 AND status='running' AND lease_until>NOW();
