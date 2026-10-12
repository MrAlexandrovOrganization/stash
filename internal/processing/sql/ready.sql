SELECT EXISTS(SELECT 1 FROM processing_workers WHERE worker_id=$1 AND instance_id=$2
 AND ready AND supports_media AND reported_at>NOW()-INTERVAL '90 seconds' AND vision_model=$3 AND text_model=$4);
