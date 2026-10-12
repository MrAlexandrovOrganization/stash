INSERT INTO processing_workers(worker_id,instance_id,ready,vision_model,text_model,supports_media)
VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(worker_id,instance_id) DO UPDATE
SET supports_media=EXCLUDED.supports_media, ready=EXCLUDED.ready, vision_model=EXCLUDED.vision_model, text_model=EXCLUDED.text_model,reported_at=NOW();
