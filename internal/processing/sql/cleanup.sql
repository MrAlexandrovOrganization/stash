DELETE FROM processing_jobs WHERE status IN ('done','failed') AND accessed_at<NOW()-INTERVAL '30 days';
DELETE FROM processing_workers WHERE reported_at<NOW()-INTERVAL '1 day';
