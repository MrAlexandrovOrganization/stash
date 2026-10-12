INSERT INTO processing_workers(worker_id,instance_id,ready,vision_model,text_model)
VALUES($1,$2,$3,$4,$5) ON CONFLICT(worker_id,instance_id) DO UPDATE
SET ready=EXCLUDED.ready, vision_model=EXCLUDED.vision_model, text_model=EXCLUDED.text_model,reported_at=NOW();
