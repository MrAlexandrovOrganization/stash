-- Vector embeddings for hybrid similarity search.
-- Requires a pgvector-enabled Postgres image (e.g. pgvector/pgvector:pg17).

CREATE EXTENSION IF NOT EXISTS vector;

-- Text embedding of the item's descriptions (multilingual model, e.g. bge-m3).
ALTER TABLE items ADD COLUMN IF NOT EXISTS embedding_text vector(1024);
-- Image embedding of the media itself (open_clip ViT-B/32, 512-dim).
ALTER TABLE items ADD COLUMN IF NOT EXISTS embedding_image vector(512);

CREATE INDEX IF NOT EXISTS items_embedding_text_idx
    ON items USING hnsw (embedding_text vector_cosine_ops);
CREATE INDEX IF NOT EXISTS items_embedding_image_idx
    ON items USING hnsw (embedding_image vector_cosine_ops);
