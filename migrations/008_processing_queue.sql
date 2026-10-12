CREATE TABLE processing_jobs (
 id TEXT PRIMARY KEY, payload BYTEA NOT NULL,
 status TEXT NOT NULL DEFAULT 'queued' CHECK(status IN ('queued','running','done','failed')),
 result BYTEA, attempts INT NOT NULL DEFAULT 0,
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(), accessed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 available_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 worker_id TEXT, instance_id TEXT, lease_id TEXT, lease_until TIMESTAMPTZ
);
CREATE INDEX processing_jobs_queue ON processing_jobs(status, available_at, created_at);
CREATE TABLE processing_workers (
 worker_id TEXT NOT NULL, instance_id TEXT NOT NULL, ready BOOLEAN NOT NULL,
 vision_model TEXT NOT NULL, text_model TEXT NOT NULL,
 reported_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 PRIMARY KEY(worker_id, instance_id)
);

ALTER TABLE items ADD COLUMN processing_generation BIGINT NOT NULL DEFAULT 1;
CREATE FUNCTION stash_processing_revision() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 NEW.processing_generation := OLD.processing_generation+1;
 NEW.embedding_text := NULL;
 RETURN NEW;
END;
$$;
CREATE TRIGGER stash_processing_revision BEFORE UPDATE OF storage_path,size,content_type,type,description,original_caption,ai_description
 ON items FOR EACH ROW EXECUTE FUNCTION stash_processing_revision();
CREATE FUNCTION stash_processing_upload() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 NEW.ai_description := NULL; NEW.ai_description_error := NULL;
 NEW.embedding_image := NULL; NEW.embedding_text := NULL;
 RETURN NEW;
END;
$$;
CREATE TRIGGER stash_processing_upload BEFORE UPDATE OF storage_path,size,content_type,type
 ON items FOR EACH ROW EXECUTE FUNCTION stash_processing_upload();
