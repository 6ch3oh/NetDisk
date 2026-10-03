#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
if [ -n "$(docker ps -a --filter 'name=^/bingyan-netdisk-full-demo$' --format '{{.Names}}')" ]; then
    printf '%s\n' 'Demo already exists. Inspect it before restarting or recreating it.' >&2
    exit 1
fi
docker compose --profile tools run --rm --no-deps --name bingyan-netdisk-full-demo-build tools sh -c 'go build -buildvcs=false -trimpath -o .tmp/full-demo/netdisk ./cmd/netdisk && go build -buildvcs=false -trimpath -o .tmp/full-demo/netdisk-p2p ./cmd/netdisk-p2p'
docker run -d --pull=never --name bingyan-netdisk-full-demo --read-only --cap-drop ALL --security-opt no-new-privileges:true \
    --mount "type=bind,source=$(pwd)/.tmp/full-demo,target=/test,readonly" \
    --mount type=volume,source=bingyan-netdisk-full-demo-data,target=/data \
    -p 127.0.0.1:38125:8080 -p 127.0.0.1:38126:2049 \
    -e NETDISK_ADDR=:8080 -e NETDISK_ORIGIN=http://127.0.0.1:38125 -e NETDISK_DATA_DIR=/data \
    -e NETDISK_DEV_EMAIL=true -e NETDISK_NFS_ADDR=0.0.0.0:2049 golang:1.27.0 /test/netdisk
printf '%s\n' 'Independent demo: http://127.0.0.1:38125 ; NFS port 38126. Local email adapter enabled.'
