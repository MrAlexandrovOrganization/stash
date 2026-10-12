SELECT EXISTS(SELECT 1 FROM processing_workers WHERE worker_id=ANY($1)
 AND ready AND reported_at>NOW()-INTERVAL '90 seconds' AND vision_model=$2 AND text_model=$3);
