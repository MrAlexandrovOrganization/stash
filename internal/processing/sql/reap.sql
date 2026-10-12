UPDATE processing_jobs SET status='failed',payload=''::bytea
WHERE status='running' AND lease_until<=NOW() AND attempts>=5;
