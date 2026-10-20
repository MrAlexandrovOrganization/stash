# server → WSL

WSL owns Stash API, PostgreSQL and MinIO alongside its existing remote worker;
no local fallback/Ollama is launched. compose/storage-wsl.yml gives the API its own persistent tailnet namespace.
API 8080 and worker RPC 50058 have no public/host port publication. Russia Caddy terminates worker
TLS and forwards gRPC over the encrypted tailnet. CLIP consumer API and Transcriber
are on Russia. Model names and vector spaces remain unchanged.

Stash-bot runs on Russia using the existing local Telegram Bot API and accesses
the Stash sidecar over the tailnet. PostgreSQL includes items, descriptions, vectors, migration
history, worker queue and counters. Copy MinIO's entire cold volume (including
.minio.sys), preserve credentials, compare all file hashes, and restore the final
pg_dump after source writers/workers stop. Keep a private copy outside both VMs.

Original source application, worker, database, bot and MinIO remain stopped with
restart=no. The source .env uses STACK_ROLE=retired to prevent normal startup.
Rollback requires stopping destination writers and moving the latest consistent
DB/object storage back; never overwrite newer data with preparation snapshots.
Shared tracing still uses the existing Jaeger on server until its separate migration.

The coordinator checkout is /home/maxim/projects/backends/stash-coordinator,
separate from the unchanged workers checkout. COMPOSE_PROJECT_NAME=stash preserves
database/network/volume identity. ASUS preparation was retired before activation
because its Wi-Fi showed packet loss and very low transfer throughput.

## Existing database index issue

A heap scan on the source finds one duplicate file_unique_id hidden by the
source's valid-marked unique index. A logical pg_restore fails when rebuilding
that index. No rows or metadata are discarded: the migration uses a final cold
physical PostgreSQL cluster archive with the exact source pgvector image digest.
All table contents are compared before API activation. Keep both physical and
logical backups; repairing this pre-existing index/data inconsistency is separate
from moving the service and requires choosing the desired duplicate semantics.
