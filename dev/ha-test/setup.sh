#!/usr/bin/env bash
# Starts (or reuses) a Home Assistant test instance in Docker, onboards it and
# writes HA_URL / HA_TOKEN to ha-test/env.
set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"

CONFIG_DIR="$HERE/config"
ENV_FILE="$HERE/env"
NAME="ha-mcp-test"
IMAGE="ghcr.io/home-assistant/home-assistant:stable"
PORT="${HA_TEST_PORT:-18124}"
URL="http://localhost:$PORT"
HA_USER="mcp"
HA_PASS="mcp-test-password"

mkdir -p "$CONFIG_DIR"

if [ ! -f "$CONFIG_DIR/configuration.yaml" ]; then
  cat >"$CONFIG_DIR/configuration.yaml" <<'EOF'
default_config:
demo:

automation: !include automations.yaml
script: !include scripts.yaml
scene: !include scenes.yaml

logger:
  default: info
EOF
fi

if [ ! -s "$CONFIG_DIR/automations.yaml" ]; then
  cat >"$CONFIG_DIR/automations.yaml" <<'EOF'
- id: "1700000000001"
  alias: Test toggle kitchen light
  description: Seed automation for integration tests
  triggers:
    - trigger: event
      event_type: mcp_test_event
  actions:
    - action: light.toggle
      target:
        entity_id: light.kitchen_lights
  mode: single
EOF
fi

if [ ! -s "$CONFIG_DIR/scripts.yaml" ]; then
  cat >"$CONFIG_DIR/scripts.yaml" <<'EOF'
seed_script:
  alias: Seed script
  sequence:
    - action: light.turn_on
      target:
        entity_id: light.ceiling_lights
  mode: single
EOF
fi

[ -f "$CONFIG_DIR/scenes.yaml" ] || echo "[]" >"$CONFIG_DIR/scenes.yaml"

if docker container inspect "$NAME" >/dev/null 2>&1; then
  if [ "$(docker container inspect -f '{{.State.Running}}' "$NAME")" != "true" ]; then
    docker start "$NAME" >/dev/null
  fi
else
  docker run -d --name "$NAME" \
    -p "127.0.0.1:$PORT:8123" \
    -v "$CONFIG_DIR:/config" \
    -e TZ=UTC \
    "$IMAGE" >/dev/null
fi

echo "Waiting for Home Assistant at $URL ..."
for _ in $(seq 1 180); do
  if curl -fsS -o /dev/null "$URL/manifest.json" 2>/dev/null; then
    break
  fi
  sleep 2
done
curl -fsS -o /dev/null "$URL/manifest.json"

if [ -f "$ENV_FILE" ]; then
  # shellcheck disable=SC1090
  . "$ENV_FILE"
  if curl -fsS -o /dev/null -H "Authorization: Bearer $HA_TOKEN" "$URL/api/"; then
    echo "Reusing existing token in $ENV_FILE"
    exit 0
  fi
fi

# The onboarding needs a WebSocket call; the HA image ships aiohttp, so run
# the flow inside the container instead of requiring local tooling.
TOKEN="$(docker exec -i -e HA_USER="$HA_USER" -e HA_PASS="$HA_PASS" "$NAME" python3 - <<'PY'
import asyncio, os, time
import aiohttp

BASE = "http://127.0.0.1:8123"
CLIENT_ID = BASE + "/"
USER, PASS = os.environ["HA_USER"], os.environ["HA_PASS"]


async def main():
    async with aiohttp.ClientSession() as s:
        async with s.get(BASE + "/api/onboarding") as r:
            # HA removes the onboarding view once every step is done.
            if r.status == 404:
                steps = {"user": True, "core_config": True, "analytics": True, "integration": True}
            else:
                steps = {x["step"]: x["done"] for x in await r.json()}

        if not steps.get("user"):
            async with s.post(BASE + "/api/onboarding/users", json={
                "client_id": CLIENT_ID, "name": "MCP Test", "username": USER,
                "password": PASS, "language": "en",
            }) as r:
                r.raise_for_status()
                code = (await r.json())["auth_code"]
        else:
            async with s.post(BASE + "/auth/login_flow", json={
                "client_id": CLIENT_ID, "handler": ["homeassistant", None],
                "redirect_uri": CLIENT_ID,
            }) as r:
                r.raise_for_status()
                flow = await r.json()
            async with s.post(BASE + "/auth/login_flow/" + flow["flow_id"], json={
                "client_id": CLIENT_ID, "username": USER, "password": PASS,
            }) as r:
                r.raise_for_status()
                code = (await r.json())["result"]

        async with s.post(BASE + "/auth/token", data={
            "grant_type": "authorization_code", "code": code, "client_id": CLIENT_ID,
        }) as r:
            r.raise_for_status()
            access = (await r.json())["access_token"]
        hdr = {"Authorization": "Bearer " + access}

        for step, body in (
            ("core_config", {}),
            ("analytics", {}),
            ("integration", {"client_id": CLIENT_ID, "redirect_uri": CLIENT_ID}),
        ):
            if steps.get(step):
                continue
            async with s.post(BASE + "/api/onboarding/" + step, json=body, headers=hdr) as r:
                if r.status not in (200, 403):
                    raise SystemExit(f"onboarding {step}: HTTP {r.status} {await r.text()}")

        async with s.ws_connect(BASE + "/api/websocket") as ws:
            await ws.receive_json()
            await ws.send_json({"type": "auth", "access_token": access})
            if (await ws.receive_json())["type"] != "auth_ok":
                raise SystemExit("websocket auth failed")
            await ws.send_json({
                "id": 1, "type": "auth/long_lived_access_token",
                # Token names must be unique per user.
                "client_name": f"mcp-integration-tests-{int(time.time())}", "lifespan": 3650,
            })
            msg = await ws.receive_json()
            if not msg.get("success"):
                raise SystemExit(f"long-lived token: {msg}")
            print(msg["result"])


asyncio.run(main())
PY
)"

umask 077
printf 'HA_URL=%s\nHA_TOKEN=%s\n' "$URL" "$TOKEN" >"$ENV_FILE"
echo "Wrote $ENV_FILE"
