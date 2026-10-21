# stash distributed workers

## Architecture

Stash HTTP API and stash-bot keep their existing contracts. Vision descriptions
and Ollama text embeddings now use Stash's PostgreSQL-backed queue. The item
remains the durable source for submission/recovery: backfill retries missing
results after process restarts. Requests are keyed by model/input; vision also
includes the persisted item generation. Results remain available across RPC
cancellation and are applied only to the current item generation. Manual
Describe persists the pending intent before starting background preparation.
Stale results cannot overwrite newer uploads or descriptions.

CLIP image embeddings and text-to-image search use the separate clip-embedder
queue. Its protobuf snapshot is committed under proto/embed; source is the
clip-embedder repository's proto/embed.proto from the coordinated worker rollout.
Ordinary builds generate the snapshot without reading a sibling checkout.

Workers call a local Ollama via HTTP because it is Ollama's supported inference
API; queue transport is gRPC. Models must match the coordinator: qwen2.5vl:3b
and bge-m3:567m (1024 dimensions). The initial Mac/WSL deployment uses CPU Ollama
in containers, with one concurrent inference and one loaded model. Mac Docker
cannot use Metal. No native background process is required.

PostgreSQL migration 008 is additive; startup preserves the established migration
behavior, now with one transaction and advisory lock covering ledger updates.
Existing descriptions/vectors are preserved. Source edits invalidate derived
vectors; no bulk reindex is started. Back up the stash PostgreSQL database before
rollout; the existing database backup must include the new queue tables.

## Worker lifecycle and security

Workers poll outbound gRPC/TLS via Caddy every five seconds. Readiness is reported
every 20 seconds, expires after 90 seconds, and is scoped to a token's worker ID.
Claimed tasks use random lease IDs, renew every 20 seconds, and become claimable
again after expiry. Completion validates owner/instance/lease and rejects stale
results. Failed attempts back off, with at most five attempts. Queued jobs have
no TTL; terminal results are retained for 30 days since last access.

Inference runs only on remote workers. The server Compose stack contains no
fallback worker; when every remote worker is offline, queued jobs wait. Models must be present before a worker reports ready.
The server stores SHA-256 token hashes in a protected credential registry;
workers mount their own raw tokens as read-only secrets. Never commit real .env,
tokens or registry files. WORKER_UID/WORKER_GID must match the owner of token
files so non-root workers can read mode-600 secrets.

Only WorkerService is public through Caddy. Consumer APIs/database/cache ports
are private. The shared workers hostname routes distinct protobuf services;
no SSH tunnels or public inbound ports on Mac/WSL are needed.

## Running

Workers, Ollama and their updater use `restart: always` so existing containers
start when Docker (including Colima) starts, even after a manual stop.
`make down` removes the containers and disables this automatic startup until
`make up`.

Copy the matching .env.server.example, .env.mac.example or .env.wsl.example to
.env and fill machine paths/endpoint. The same make up/down/logs/restart/ps
commands select the server or worker Compose stack. .env sets models, IDs,
profiles, resource limits, tokens and autodeploy. make config-check validates it.

For CPU Ollama, first start the stack with make up, then prepare its named model
volume using make models. Until both models are present, the worker is not ready
and the permanent server fallback continues accepting tasks. Model downloads
run only through this explicit command, never during checks or hooks.

## CI and autodeploy

CI uses Make to check, build and publish immutable CPU (amd64/arm64)
images to public GHCR packages. Only after the server deployment succeeds is the
stable release marker promoted. The server consumes the already published amd64
CPU artifact by immutable digest with `make deploy-image`; it does not rebuild
application code. `make server-image` resolves that artifact for the exact SHA. WORKER_AUTODEPLOY=1 enables the container updater:
it downloads the approved digest, drains a busy worker, starts the replacement,
waits for Docker health, and rolls back on startup failure. Offline devices catch
up after returning. Machine .env and token mounts are preserved. The updater has
Docker socket access and manages only explicitly labelled workers in its own
Compose project. Its bootstrap source is vendored from transcriber's approved
updater at cd4dcb2393fdc159f76cb98570f99b724edc2384, adapted for these images.

make install installs pinned tools/dependencies; make format applies formatting;
make check is read-only analysis and existing Go tests. Models, inference, production queues and Telegram
are not used by ordinary checks. make versions shows tool/image pins. Version
sources are makefiles/versions.mk, go.mod or pyproject.toml/uv.lock. Standalone worker
health reflects readiness reports; docker logs includes structured lifecycle,
attempt outcome and duration without media, descriptions or bearer credentials.

## Monitoring

The private Prometheus endpoint binds only its prometheus-net alias, with no
published host port or Caddy route. Scrape failures return HTTP 503; stale values
are never emitted as a successful scrape. The coordinator snapshots queue state
and the latest compatible readiness reports, with explicit zero series for all
credential-registry worker IDs. Metrics contain no media, text, task IDs, token
values or instance UUID labels. Readiness has the same 90-second TTL as claiming.

Completion, failure, retry and lease-expiration counters are stored transactionally
with queue state and survive process restarts and terminal-job cleanup. Their
history starts at this rollout; old results are not retroactively counted.
P50/P95 duration gauges use successful jobs completed in the last 24 hours,
measured from coordinator lease acquisition and enqueue time respectively.
They include transport/model-loading time and are not device benchmarks.
Legacy results without recorded timestamps do not contribute to durations.

Startup runs the additive monitoring migration under the existing database lock;
source items and queue payload/results are preserved. This is an explicit
continuation of this project's startup-migration policy. Backups must include
queue metadata and counters as well as stored results.

`make monitoring-install` installs only this project's files into
MONITORING_DATA_DIR (default ~/.monitoring). Server `make up` calls it automatically;
worker stacks never change shared monitoring files. The existing Prometheus
file discovery and Grafana file provisioning pick up changes without restarts.

`make dashboards` renders the shared Stash/CLIP dashboard from
scripts/render_dashboards.py; `make monitoring-check` checks generated JSON and
all panel PromQL expressions with pinned promtool. Grafana URL: /d/compute-workers.
The dashboard's editable source belongs to Stash; CLIP owns its metrics contract
and scrape target. CPU/GPU devices need no inbound monitoring ports.

## Media preparation and embedding recovery

Backfill selects missing image vectors, and missing text vectors only when the
item has nonempty source text. Each embedding branch checks its stored vector
before loading media or submitting inference. Metadata/description changes still
invalidate text vectors through the existing generation trigger. Completed image
vectors are retained until the source media changes.

The server queues object references for vision and video/GIF frame preparation.
Workers download inputs over lease-authenticated `DownloadMedia` gRPC/TLS;
only the object referenced by an active lease is accessible. Workers receive no
MinIO credentials, signed URLs or arbitrary object-read API. Downloads are streamed
in 64 KiB chunks to temporary worker files, limited to 512 MiB and ten minutes.
Lease checks recur during download; normal heartbeats continue during preparation.
FFmpeg runs on the claiming worker, using the existing frame sampling and JPEG
encoding, with a single decoder thread. Prepared frames are limited to 16 MiB in
total, matching the existing task input bound. For CLIP the worker returns frames
to Stash, which submits the middle frame to the separate CLIP inference queue.
Queued object references and prepared results survive coordinator restarts.

Migration 010 adds a media capability flag, default false for existing reports.
The new coordinator accepts claims only from compatible media-capable instances;
old workers wait without consuming unsupported jobs. CI publishes the images,
deploys the coordinator, then promotes the approved release. Mac/WSL updaters
replace workers automatically; the updated server fallback covers their absence.
Existing queued text/frame-based vision tasks remain supported. Roll back code
without dropping the additive column; restore the previous approved worker image
if rolling back both sides.

`WORKER_MEMORY_LIMIT` (default 512m) and `WORKER_CPU_LIMIT` (default 0.5 CPUs)
control the worker process plus FFmpeg, independently of Ollama. Mac/WSL examples
use two CPUs; existing .env files retain the default until explicitly tuned.

`make proto-check` builds, formats without writes, lints and checks FILE-level
protobuf compatibility against `PROTO_BREAKING_BASE` (default HEAD^). CI supplies
the PR base or previous push SHA and fetches history; a missing base is an error.
Generated Go files remain ignored and are regenerated from committed protos.

## Repository layout and local operation

The root contains one Makefile. Version pins live in `makefiles/versions.mk`.
Compose configurations live in `compose/`:

- `server.yml`: coordinator and persistent storage, without an inference worker.
- `workers.yml`: device workers and their model cache/runtime.
- `workers.autodeploy.yml`: optional approved-release updater.

Use Make commands from the checkout root; Compose uses an explicit project
directory so relative paths, project names and named volumes survive this move.
Copy the appropriate machine example to `.env`; keep existing secrets and volumes
when updating an existing machine. `make config-check-examples` validates all
examples without reading production secrets. `make config-check` validates the
selected role, profiles, TLS transport, worker identities and secret mounts.
Remote workers use TLS and individual credentials. The server fallback's
plaintext transport is restricted to its private Compose service endpoint.

`make run` starts the coordinator in the foreground using exported configuration;
Ctrl-C stops it. The coordinator requires the database/storage configuration and
credential registry described in the server example. Prefer `make up` to run the
complete stack; `make down` stops it without deleting named volumes. Model downloads
and inference are excluded from ordinary checks.

Committed protobuf snapshots are regenerated with `make generate`; ordinary
builds never synchronize from a neighboring checkout. Refresh a consumer snapshot
explicitly from its canonical repository and review compatibility before delivery.

Successful CI deployment records its immutable image in ignored
`.server-release.env`; subsequent Make commands retain it. The previous value is
kept in `.server-release.previous.env`. For a manual rollback, first select the
compatible Git revision, then pass its saved digest as `SERVER_IMAGE` to
`make deploy-image`; database migrations/data are not rolled back automatically.
Keep the pre-migration container images until the first successful rollout.

CI actions use the Node.js 24 runtime. This concerns the actions themselves,
not the application's Go/Python runtime. GitHub-hosted runners supply it;
self-hosted runners must meet the selected actions' minimum runner version.

## Container names

Compose uses explicit service names without replica numbers. Set
`CONTAINER_NAME_PREFIX` (for example `dev-`) for an additional local or CI stack
on the same Docker host. Keep Compose project/service labels and volume names
unchanged; clients address services or explicit network aliases.

## Migration from server

See [MIGRATION.md](MIGRATION.md) for the placement, data verification and rollback.

The WSL coordinator uses a separate stash-coordinator checkout and a dedicated
Tailscale sidecar. CI reaches native WSL SSH through a key restricted to the
deployment receiver on Russia; Git fetch uses the public HTTPS origin.

Migration 011 rebuilds the inconsistent legacy `file_unique_id` index without
deleting items/files or changing their original metadata, timestamps or vectors.
For each duplicated Telegram ID, the newest-created item (then greatest item ID
on a tie) owns re-upload deduplication. Older copies retain their `file_unique_id`
and are marked `file_unique_id_legacy_duplicate` in PostgreSQL; they remain
available through ordinary item listing, search, download, edit and deletion.
The unique index covers only deduplication owners, including all new items.
Re-sending updates the owner; deleting it does not promote a historical copy:
the next upload creates a new owner. The marker is operational database metadata
and does not change the existing JSON API. A fresh database backup is required
before rollout; startup applies this migration atomically under the existing lock.
An older binary must not be deployed after 011 because its unfiltered lookup could
choose a historical copy. Restore a compatible binary or forward-fix it.

`make migration-test` (also part of `make check`) runs synthetic duplicate/null
fixtures, the embedded repository lookup, btree heap coverage and a complete
logical dump/restore in a temporary pinned PostgreSQL container. It needs Docker,
publishes no ports, mounts no host data and never reads application DB settings.
