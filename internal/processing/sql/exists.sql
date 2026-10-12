SELECT EXISTS(SELECT 1 FROM processing_jobs WHERE id=$1);
