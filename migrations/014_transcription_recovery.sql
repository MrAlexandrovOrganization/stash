-- Independent from processing_generation: descriptions/embeddings can change
-- while transcribing, but new media or an explicit transcript edit must fence it.
ALTER TABLE items ADD COLUMN transcript_generation BIGINT NOT NULL DEFAULT 1;
CREATE FUNCTION stash_transcript_revision() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    NEW.transcript_generation := OLD.transcript_generation + 1;
    RETURN NEW;
END;
$$;
CREATE TRIGGER stash_transcript_revision BEFORE UPDATE OF storage_path, transcript
    ON items FOR EACH ROW EXECUTE FUNCTION stash_transcript_revision();

CREATE TABLE transcription_tasks (
    request_id TEXT PRIMARY KEY,
    item_id TEXT NOT NULL,
    generation BIGINT NOT NULL,
    storage_path TEXT NOT NULL,
    audio_format TEXT NOT NULL,
    job_id TEXT,
    state TEXT NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'ack', 'done')),
    terminal_status TEXT CHECK (terminal_status IN ('done', 'failed')),
    result_text TEXT,
    attempts INT NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (item_id, generation)
);
CREATE INDEX transcription_tasks_pending_idx ON transcription_tasks (state, next_attempt_at);
CREATE INDEX transcription_tasks_media_idx ON transcription_tasks (storage_path) WHERE state = 'pending';
-- No item FK: re-upload/deletion must not lose an accepted job or its pending Ack.
