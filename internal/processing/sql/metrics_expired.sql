SELECT COUNT(*) FROM processing_jobs WHERE status='running' AND lease_until<=NOW();
