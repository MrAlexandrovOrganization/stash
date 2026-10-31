-- Expiry is immutable and separate from GC retry scheduling. Retrying deletion
-- must never revive permission to publish an object that may already be gone.
-- NULL marks retired/pre-expiry journal entries; only fresh reservations can
-- publish. Existing entries remain eligible for cleanup after their grace period.
ALTER TABLE file_gc ADD COLUMN upload_expires_at TIMESTAMPTZ;
