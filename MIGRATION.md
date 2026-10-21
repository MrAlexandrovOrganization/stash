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

## Database index issue at cutover

A heap scan on the source finds one duplicate file_unique_id hidden by the
source's valid-marked unique index. A logical pg_restore fails when rebuilding
that index. No rows or metadata are discarded: the migration uses a final cold
physical PostgreSQL cluster archive with the exact source pgvector image digest.
All table contents are compared before API activation. Keep both physical and
logical backups. The follow-up repair below resolves this inconsistency.

## Verified on 21 October 2026

The final physical cluster restored 954 items, 452 processing jobs, two worker
records, ten counters and ten migration entries. Canonical row hashes matched
every source table before API startup. All 1,447 files in the cold MinIO volume
matched; two existing downloads matched through the new API.
A synthetic image completed CLIP 512, BGE 1024 and Qwen vision processing on the
remote worker, then the test item/object were deleted. No original records were
removed. The source unique-index inconsistency was initially preserved at cutover.

The API tailnet address is 100.64.0.7 (stash-storage-wsl), persisted across sidecar
recreation. PostgreSQL, MinIO and API run in separate projects/roles from the
existing WSL workers. The coordinator checkout is backends/stash-coordinator.
CI relays through Russia to native WSL SSH on 100.64.0.3:22; the SSH key can only
run this deployment receiver. DEPLOY_ENABLED=true enables delivery.

## Index repair on 21 October 2026

Migration 011 preserves both copies, every original field and every object. The
newest-created copy owns re-upload deduplication; the other has only the new
`file_unique_id_legacy_duplicate` marker set. The rebuilt partial unique index and
repository lookup use that marker. See README for deletion/re-upload semantics and
the restriction against rolling back to the old unfiltered lookup.

The deployed migration preserved all 954 item rows byte-for-byte after excluding
the added marker (canonical JSON SHA-256
`128dfd111b020c5bc65b8ed102a2cacfd4ecb08dbe9dc14f4e7b3ac250486b4f`).
All 1,404 files under the MinIO stash bucket matched the pre-cutover manifest,
with no missing/changed/added files. PostgreSQL amcheck passed all 55 checked
public/TOAST btrees; heap/TOAST verification found no errors in the five public
tables. A fresh full logical dump restored successfully into an isolated pinned
PostgreSQL container, with the same 954-row hash and a valid rebuilt unique index.

Private fresh before/after dumps and the fresh MinIO archive are under
`/home/maxim/backups/stash-repair-20261021/` on WSL. Both dumps and fingerprints
are copied to the Mac's `~/.local/state/stash-repair-20261021/`; the complete
off-VM MinIO archive remains in `~/.local/state/server-migration-20261020/` and
its bucket-file manifest matches current storage. No old snapshot was activated.
