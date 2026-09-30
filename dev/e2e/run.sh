#!/usr/bin/env bash
# Runs the sign-in flows in Chromium through Playwright's Docker image.
# Needs the test Home Assistant from dev/ha-test/setup.sh, or HA_URL and
# HA_TOKEN in the environment.
set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd -P)"
ROOT="$(dirname "$(dirname "$HERE")")"
IMAGE="mcr.microsoft.com/playwright/python:v1.63.0-noble"

if [ -z "${HA_URL:-}" ]; then
  set -a
  . "$ROOT/dev/ha-test/env"
  set +a
fi
# Inside the container, localhost is the container itself.
HA_URL="${HA_URL/localhost/host.docker.internal}"
HA_URL="${HA_URL/127.0.0.1/host.docker.internal}"

ARCH="$(docker version --format '{{.Server.Arch}}')"
mkdir -p "$HERE/.bin"
CGO_ENABLED=0 GOOS=linux GOARCH="$ARCH" go -C "$ROOT" build -o "$HERE/.bin/mcp-for-homeassistant" ./cmd/mcp-for-homeassistant

docker run --rm --init \
  -e HA_URL="$HA_URL" -e HA_TOKEN="$HA_TOKEN" \
  -v "$HERE:/work" -w /work \
  "$IMAGE" sh -c "pip install --quiet --break-system-packages playwright==1.63.0 && python3 signin.py"
