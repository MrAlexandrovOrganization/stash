ALTER TABLE processing_jobs ADD COLUMN attempt_started_at TIMESTAMPTZ;
ALTER TABLE processing_jobs ADD COLUMN finished_at TIMESTAMPTZ;
CREATE INDEX processing_jobs_finished ON processing_jobs(finished_at) WHERE finished_at IS NOT NULL;
CREATE TABLE processing_counters (
 worker_id TEXT NOT NULL, event TEXT NOT NULL, value BIGINT NOT NULL DEFAULT 0,
 PRIMARY KEY(worker_id,event),
 CHECK(event IN ('completed','failed','attempt_failed','retry','lease_expired'))
);
CREATE FUNCTION stash_processing_count(w TEXT,e TEXT) RETURNS void LANGUAGE sql AS $$
 INSERT INTO processing_counters(worker_id,event,value) VALUES(COALESCE(w,'other'),e,1)
 ON CONFLICT(worker_id,event) DO UPDATE SET value=processing_counters.value+1;
$$;
CREATE FUNCTION stash_processing_metrics() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.status='running' AND NEW.lease_id IS DISTINCT FROM OLD.lease_id THEN
  NEW.attempt_started_at:=NOW(); NEW.finished_at:=NULL;
  IF NEW.attempts>1 THEN PERFORM stash_processing_count(NEW.worker_id,'retry'); END IF;
  IF OLD.status='running' THEN PERFORM stash_processing_count(OLD.worker_id,'lease_expired'); END IF;
 END IF;
 IF NEW.status='done' AND OLD.status<>'done' THEN
  NEW.finished_at:=NOW(); PERFORM stash_processing_count(NEW.worker_id,'completed');
 END IF;
 IF OLD.status='running' AND NEW.status IN ('queued','failed') THEN
  IF OLD.lease_until<=NOW() THEN
   PERFORM stash_processing_count(OLD.worker_id,'lease_expired');
  ELSE
   PERFORM stash_processing_count(OLD.worker_id,'attempt_failed');
  END IF;
 END IF;
 IF NEW.status='failed' AND OLD.status<>'failed' THEN
  NEW.finished_at:=NOW(); PERFORM stash_processing_count(NEW.worker_id,'failed');
 END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER stash_processing_metrics BEFORE UPDATE ON processing_jobs
 FOR EACH ROW EXECUTE FUNCTION stash_processing_metrics();
