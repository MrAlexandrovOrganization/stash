SELECT pg_advisory_xact_lock(hashtextextended('stash:upload:' || $1, 0));
