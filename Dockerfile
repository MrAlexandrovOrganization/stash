# check=skip=InvalidDefaultArgInFrom
ARG GO_VERSION
ARG ALPINE_VERSION
FROM golang:${GO_VERSION}-alpine AS builder
RUN apk add --no-cache make
WORKDIR /app
COPY go.mod go.sum Makefile versions.mk ./
RUN go mod download && make install
COPY . .
RUN make generate && CGO_ENABLED=0 go build -o /out/stash ./cmd/stash && CGO_ENABLED=0 go build -o /out/stash-worker ./cmd/stash-worker
FROM alpine:${ALPINE_VERSION}
RUN apk add --no-cache ca-certificates ffmpeg && adduser -D -u 10001 app
WORKDIR /app
COPY --from=builder /out/* /app/
USER app
EXPOSE 8080
ENTRYPOINT ["/app/stash"]
