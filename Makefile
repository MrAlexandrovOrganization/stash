DOCKER_COMPOSE = docker compose
include Makefiles/versions.mk
-include .worker-release.env
-include .server-release.env
export WORKER_CPU_IMAGE WORKER_GPU_IMAGE
ENV_FILE ?= .env
STACK_ROLE ?= server
ifneq (,$(wildcard $(ENV_FILE)))
include $(ENV_FILE)
export
endif
ifeq ($(STACK_ROLE),workers)
COMPOSE_FILE := compose/workers.yml
ifeq ($(WORKER_AUTODEPLOY),1)
COMPOSE_FILE := compose/workers.yml:compose/workers.autodeploy.yml
endif
else
COMPOSE_FILE := compose/server.yml
endif
export COMPOSE_FILE STACK_ROLE WORKER_AUTODEPLOY COMPOSE_PROFILES
DOCKER_COMPOSE += --project-directory "$(CURDIR)" --env-file $(ENV_FILE)
BINARY = stash

# Install all dev tools: buf + Go protoc plugins.
# buf install: https://buf.build/docs/installation
#   macOS: brew install bufbuild/buf/buf
.PHONY: install
install:
	go install github.com/bufbuild/buf/cmd/buf@v$(BUF_VERSION)
	go install google.golang.org/protobuf/cmd/protoc-gen-go@$(PROTOC_GEN_GO_VERSION)
	go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v$(PROTOC_GEN_GO_GRPC_VERSION)

# Regenerate Go stubs from the committed protos (no sync).
# All committed snapshots generate through Buf; no standalone protoc.
# Requires pinned tools from make install.
.PHONY: generate
generate:
	buf generate

.PHONY: proto-check
PROTO_BREAKING_BASE ?= HEAD^
proto-check:
	buf build proto
	buf format --diff --exit-code proto
	buf lint proto
	git cat-file -e "$(PROTO_BREAKING_BASE)^{commit}"
	buf breaking proto --against ".git#ref=$(PROTO_BREAKING_BASE),subdir=proto"

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
up-selected: config-check
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
	python3 scripts/check_config.py $(DOCKER_COMPOSE)
compose-build:
	$(DOCKER_COMPOSE) build
check: generate proto-check
	@test -z "$$(gofmt -l cmd internal)" || { gofmt -l cmd internal; exit 1; }
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

.PHONY: config-check-examples
config-check-examples:
	$(MAKE) ENV_FILE=.env.server.example config-check
	$(MAKE) ENV_FILE=.env.mac.example config-check
	$(MAKE) ENV_FILE=.env.wsl.example config-check

check: config-check-examples

.PHONY: run test-race
run: generate
	go run ./cmd/stash
test-race: generate
	go test -race ./...

# CI deploys the already built amd64 artifact by immutable digest.
.PHONY: server-image deploy-image
server-image:
	python3 scripts/server_image.py
deploy-image: config-check
	@python3 -c 'import os,re; assert re.fullmatch(re.escape(os.environ["WORKER_REGISTRY"]+"-worker-cpu")+r"@sha256:[a-f0-9]{64}",os.environ.get("SERVER_IMAGE","")), "SERVER_IMAGE must be the published immutable digest"'
	$(DOCKER_COMPOSE) up -d --no-build --pull always --wait --wait-timeout 300
	$(MAKE) monitoring-install
	python3 scripts/server_image.py record
export SERVER_IMAGE
