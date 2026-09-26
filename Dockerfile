FROM golang:1.26.7-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o stash ./cmd/stash

FROM alpine:3.21
# ffmpeg extracts video frames for AI descriptions.
RUN apk add --no-cache ca-certificates ffmpeg
WORKDIR /app
COPY --from=builder /app/stash .
EXPOSE 8080
ENTRYPOINT ["./stash"]
