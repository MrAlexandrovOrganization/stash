# server → ASUS + WSL

ASUS (100.64.0.11) owns Stash API, PostgreSQL and MinIO. Inference remains on WSL;
no local fallback/Ollama is launched. compose/asus.yml publishes API 8080 and
worker RPC 50058 only to ASUS's tailnet address. Russia Caddy terminates worker
TLS and forwards gRPC over the encrypted tailnet. CLIP consumer API and Transcriber
are on Russia. Model names and vector spaces remain unchanged.

Stash-bot runs on Russia using the existing local Telegram Bot API and accesses
ASUS over the tailnet. PostgreSQL includes items, descriptions, vectors, migration
history, worker queue and counters. Copy MinIO's entire cold volume (including
.minio.sys), preserve credentials, compare all file hashes, and restore the final
pg_dump after source writers/workers stop. Keep a private copy outside both VMs.

Original source application, worker, database, bot and MinIO remain stopped with
restart=no. The source .env uses STACK_ROLE=retired to prevent normal startup.
Rollback requires stopping destination writers and moving the latest consistent
DB/object storage back; never overwrite newer data with preparation snapshots.
Shared tracing still uses the existing Jaeger on server until its separate migration.
