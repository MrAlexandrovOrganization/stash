SELECT payload FROM processing_jobs WHERE id=$1 AND worker_id=$2 AND instance_id=$3 AND lease_id=$4 AND status='running' AND lease_until>NOW();
