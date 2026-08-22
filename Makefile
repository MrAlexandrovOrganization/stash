DOCKER_COMPOSE = docker compose
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
	go install github.com/bufbuild/buf/cmd/buf@v1.67.0
	go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.11
	go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.6.1

# Sync proto from the canonical source (backends/transcriber) and regenerate Go stubs.
# Requires: buf, protoc-gen-go, protoc-gen-go-grpc  →  make install
.PHONY: proto
proto:
	@echo "Syncing proto from $(WHISPER_PROTO_SRC)..."
	@mkdir -p proto gen/whisper
	@if echo "$(WHISPER_PROTO_SRC)" | grep -qE "^https?://"; then \
		curl -sSfL "$(WHISPER_PROTO_SRC)" -o proto/whisper.proto; \
	else \
		cp "$(WHISPER_PROTO_SRC)" proto/whisper.proto; \
	fi
	sed -i '' 's|option go_package = ".*";|option go_package = "stash/gen/whisper";|' proto/whisper.proto
	buf generate

.PHONY: proto-lint
proto-lint:
	buf lint proto

# Regenerate Go stubs from the committed proto (no sync).
# Requires: buf, protoc-gen-go, protoc-gen-go-grpc  →  make install
.PHONY: generate
generate:
	mkdir -p gen/whisper
	buf generate

.PHONY: format
format:
	@test -z "$$(gofmt -l .)" || (gofmt -l . && exit 1)

.PHONY: build
build:
	go build -o $(BINARY) ./cmd/stash

# The backend stack owns ONLY the "stash" service (postgres starts as its
# dependency). It must never start the bot — that lives in the stash-bot repo.
# Targeting "stash" explicitly prevents accidentally bringing up other services.
.PHONY: up
up:
	$(DOCKER_COMPOSE) up -d --build stash

.PHONY: down
down:
	$(DOCKER_COMPOSE) down

.PHONY: logs
logs:
	$(DOCKER_COMPOSE) logs -f stash

.PHONY: deploy
deploy:
	$(DOCKER_COMPOSE) up -d --build --no-cache stash

.PHONY: restart
restart:
	$(DOCKER_COMPOSE) restart stash

.PHONY: test
test:
	go test ./...

.PHONY: vet
vet:
	go vet ./...

.PHONY: clean
clean:
	rm -f $(BINARY)
