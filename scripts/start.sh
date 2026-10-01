#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
if [ "$(docker compose ps --status running --services app)" = "app" ]; then
    printf '%s\n' 'Already running: http://127.0.0.1:38120 (web and API)'
    exit 0
fi
# Verify the host bind using a short-lived project container before building.
# The final bind is also atomic; neither operation stops an existing listener.
docker run --rm --pull=never --name bingyan-netdisk-portcheck -p 127.0.0.1:38120:8080 golang:1.27.0 true
docker compose --profile tools run --rm --no-deps tools go build -trimpath -o bin/netdisk ./cmd/netdisk
docker compose up -d app
printf '%s\n' 'Local URL: http://127.0.0.1:38120 (web and API)'
