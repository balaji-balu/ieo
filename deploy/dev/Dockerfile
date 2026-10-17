# syntax=docker/dockerfile:1
# Builds one IEO binary from the checkout for the laptop harness (ADR 0009). compose.yaml picks it:
#   build: {context: ../.., dockerfile: deploy/dev/Dockerfile, args: {CMD: co}}
# BASE is the image it runs in. The EN runs the Compose CLI (ADR 0003), so it takes docker:27-cli,
# which is Alpine with the Docker CLI and its Compose plugin.
ARG BASE=alpine:3.20
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG CMD
RUN test -n "$CMD" && CGO_ENABLED=0 go build -o /out/app ./cmd/${CMD}

# Alpine, not distroless: its busybox wget serves the compose healthchecks.
FROM ${BASE}
COPY --from=build /out/app /usr/local/bin/app
ENTRYPOINT ["/usr/local/bin/app"]
CMD []
