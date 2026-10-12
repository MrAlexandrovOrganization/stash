UPDATE processing_jobs SET status='done',result=$5,payload=''::bytea,accessed_at=NOW()
WHERE id=$1 AND worker_id=$2 AND instance_id=$3 AND lease_id=$4
AND ((status='running' AND lease_until>NOW()) OR (status='done' AND result=$5));
