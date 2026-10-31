-- Upload intent is committed before MinIO I/O. Publishing an item removes its
-- intent and journals its previous key in the same transaction. Uncertain commit
-- outcomes therefore never require deleting an object from the request handler.
CREATE TABLE file_gc (
    storage_path TEXT PRIMARY KEY,
    not_before TIMESTAMPTZ NOT NULL DEFAULT (NOW() + INTERVAL '1 hour')
);
CREATE INDEX file_gc_due_idx ON file_gc (not_before);
CREATE INDEX items_storage_path_idx ON items (storage_path);
