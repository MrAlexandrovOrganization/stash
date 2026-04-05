CREATE EXTENSION IF NOT EXISTS pg_trgm;

CREATE TABLE IF NOT EXISTS items (
    id                TEXT PRIMARY KEY,
    type              TEXT NOT NULL CHECK (type IN ('image', 'video', 'gif', 'document')),
    file_name         TEXT NOT NULL,
    content_type      TEXT NOT NULL,
    size              BIGINT NOT NULL,
    storage_path      TEXT NOT NULL,
    description       TEXT NOT NULL DEFAULT '',
    tags              TEXT[] NOT NULL DEFAULT '{}',
    transcript        TEXT,
    transcript_job_id TEXT,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS items_tags_idx
    ON items USING GIN(tags);

CREATE INDEX IF NOT EXISTS items_description_trgm_idx
    ON items USING GIN(description gin_trgm_ops);

CREATE INDEX IF NOT EXISTS items_description_ts_idx
    ON items USING GIN(to_tsvector('russian', description));

CREATE INDEX IF NOT EXISTS items_transcript_ts_idx
    ON items USING GIN(to_tsvector('russian', coalesce(transcript, '')));

CREATE INDEX IF NOT EXISTS items_transcript_job_idx
    ON items (transcript_job_id)
    WHERE transcript_job_id IS NOT NULL;
