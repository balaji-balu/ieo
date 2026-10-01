# syntax=docker/dockerfile:1
# Builds one IEO binary from the checkout for the laptop harness (ADR 0009). compose.yaml picks it:
#   build: {context: ../.., dockerfile: deploy/dev/Dockerfile, args: {CMD: co}}
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG CMD
RUN test -n "$CMD" && CGO_ENABLED=0 go build -o /out/app ./cmd/${CMD}

# Alpine, not distroless: its busybox wget serves the compose healthchecks.
FROM alpine:3.20
COPY --from=build /out/app /usr/local/bin/app
ENTRYPOINT ["/usr/local/bin/app"]
