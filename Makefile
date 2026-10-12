DOCKER_COMPOSE = docker compose
include versions.mk
-include .worker-release.env
export WORKER_CPU_IMAGE WORKER_GPU_IMAGE
ENV_FILE ?= .env
STACK_ROLE ?= server
ifneq (,$(wildcard $(ENV_FILE)))
include $(ENV_FILE)
export
endif
ifeq ($(STACK_ROLE),workers)
COMPOSE_FILE := compose.workers.yml
ifeq ($(WORKER_AUTODEPLOY),1)
COMPOSE_FILE := compose.workers.yml:compose.autoupdate.yml
endif
else
COMPOSE_FILE := docker-compose.yml
endif
export COMPOSE_FILE
DOCKER_COMPOSE += --env-file $(ENV_FILE)
BINARY = stash

# Canonical source of whisper.proto.
# For remote fetch (e.g. in CI without access to the backend repo):
#   make proto WHISPER_PROTO_SRC=https://raw.githubusercontent.com/org/transcriber/main/proto/whisper.proto
WHISPER_PROTO_SRC ?= ../transcriber/proto/whisper.proto

# Install all dev tools: buf + Go protoc plugins.
# buf install: https://buf.build/docs/installation
#   macOS: brew install bufbuild/buf/buf
.PHONY: install
install:
	go install github.com/bufbuild/buf/cmd/buf@v$(BUF_VERSION)
	go install google.golang.org/protobuf/cmd/protoc-gen-go@$(PROTOC_GEN_GO_VERSION)
	go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v$(PROTOC_GEN_GO_GRPC_VERSION)

# Sync proto from the canonical source (backends/transcriber) and regenerate Go stubs.
# Requires: buf, protoc-gen-go, protoc-gen-go-grpc  →  make install
.PHONY: proto
proto:
	@echo "Syncing proto from $(WHISPER_PROTO_SRC)..."
	@mkdir -p proto/whisper gen/whisper
	@if echo "$(WHISPER_PROTO_SRC)" | grep -qE "^https?://"; then \
		curl -sSfL "$(WHISPER_PROTO_SRC)" -o proto/whisper/whisper.proto; \
	else \
		cp "$(WHISPER_PROTO_SRC)" proto/whisper/whisper.proto; \
	fi
	sed -i '' 's|option go_package = ".*";|option go_package = "stash/gen/whisper";|' proto/whisper/whisper.proto
	buf generate

# Regenerate Go stubs from the committed protos (no sync).
# whisper -> gen/whisper (buf); embed -> gen/embed (protoc, scoped).
# Requires: buf, protoc, protoc-gen-go, protoc-gen-go-grpc  →  make install
.PHONY: generate
generate:
	buf generate

.PHONY: proto-lint
proto-lint:
	buf lint proto

.PHONY: format
format:
	gofmt -w cmd internal
	buf format -w proto

.PHONY: build
build:
	go build -o $(BINARY) ./cmd/stash
	go build -o stash-worker ./cmd/stash-worker

# The backend stack owns ONLY the "stash" service (postgres starts as its
# dependency). It must never start the bot — that lives in the stash-bot repo.
# Targeting "stash" explicitly prevents accidentally bringing up other services.
.PHONY: up
up:
ifeq ($(STACK_ROLE):$(WORKER_AUTODEPLOY),workers:1)
	$(MAKE) worker-release
	$(MAKE) up-selected
else
	$(MAKE) up-selected
endif
up-selected:
	$(DOCKER_COMPOSE) up -d --build
ifeq ($(STACK_ROLE),server)
	$(MAKE) monitoring-install
endif

.PHONY: down
down:
	$(DOCKER_COMPOSE) down

.PHONY: logs
logs:
	$(DOCKER_COMPOSE) logs -f

.PHONY: deploy
deploy:
	$(DOCKER_COMPOSE) up -d --build --no-cache stash

.PHONY: restart
restart:
	$(DOCKER_COMPOSE) restart

.PHONY: test
test:
	go test ./...

.PHONY: vet
vet:
	go vet ./...

.PHONY: clean
clean:
	rm -f $(BINARY)

.PHONY: ps config-check compose-build check versions
ps:
	$(DOCKER_COMPOSE) ps -a
config-check:
	$(DOCKER_COMPOSE) config --quiet
compose-build:
	$(DOCKER_COMPOSE) build
check: generate
	@test -z "$$(gofmt -l cmd internal)" || { gofmt -l cmd internal; exit 1; }
	buf format --diff --exit-code proto
	buf lint proto
	go vet ./...
	go test ./...
versions:
	@$(foreach v,$(VERSION_VARS),printf '%s=%s\n' '$(v)' '$($(v))';)
build test vet: generate

WORKER_REGISTRY := ghcr.io/mralexandrovorganization/stash
SOURCE_REVISION ?= $(shell git rev-parse HEAD)
export WORKER_REGISTRY SOURCE_REVISION
.PHONY: worker-release publish-workers promote-workers up-selected
worker-release:
	python3 scripts/worker_release.py resolve
publish-workers:
	python3 scripts/worker_release.py publish --arch $(PUBLISH_ARCH)
promote-workers:
	python3 scripts/worker_release.py promote

.PHONY: models
models:
	@test '$(STACK_ROLE)' = workers
	$(DOCKER_COMPOSE) exec ollama ollama pull '$(OLLAMA_MODEL)'
	$(DOCKER_COMPOSE) exec ollama ollama pull '$(OLLAMA_EMBED_MODEL)'

.PHONY: dashboards monitoring-check monitoring-install
monitoring-install:
	@test '$(STACK_ROLE)' = server
	python3 scripts/install_monitoring.py
dashboards:
	python3 scripts/render_dashboards.py
monitoring-check:
	python3 scripts/render_dashboards.py --check
	python3 scripts/render_dashboards.py --rules | docker run --rm -i --entrypoint promtool prom/prometheus:$(PROMETHEUS_CHECK_VERSION) check rules /dev/stdin
check: monitoring-check
