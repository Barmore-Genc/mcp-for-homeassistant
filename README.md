# mcp-for-homeassistant

An MCP server that gives an AI agent access to your
[Home Assistant](https://www.home-assistant.io/) instance. It runs as one small
container, holds no state, and needs a Home Assistant URL and token plus a way
to sign you in: either a username and password you choose, or an identity
provider it delegates to over OpenID Connect.

## Running it

```sh
docker run -p 127.0.0.1:8080:8080 \
  -e HA_URL=http://homeassistant:8123 \
  -e HA_TOKEN=... \
  -e MCP_USERNAME=you \
  -e MCP_PASSWORD='a long password' \
  -e MCP_SIGNING_KEY="$(openssl rand -base64 32)" \
  -e MCP_ORIGIN=https://ha-mcp.example.com \
  ghcr.io/barmore-genc/mcp-for-homeassistant:latest
```

Generate `MCP_SIGNING_KEY` once and keep it: every token the server issues is
signed with it, so a new key signs every connected agent out.

With Docker Compose, copy `docker-compose.example.yml`, fill in the values and
run `docker compose up -d`.

Create the token in Home Assistant under your profile, **Security**, **Long-lived
access tokens**. The token must belong to an admin user. The tools that read and
edit configuration use endpoints Home Assistant restricts to admins, and they
fail with a permission error for any other user.

Only automations, scripts and scenes managed in the Home Assistant UI can be
edited. These are the ones stored in `automations.yaml`, `scripts.yaml` and
`scenes.yaml` with an `id`. Definitions written by hand in `configuration.yaml`,
in packages or in other included files are read-only to the API and cannot be
changed through this server.

`MCP_ORIGIN` is the public URL this server is reached at, scheme included. It
has to be right: the OAuth flow builds every URL it advertises from it. It must
be `https://`, except for `localhost` or a loopback address while trying the
server out on your own machine.

The examples publish the port on `127.0.0.1` only. Put a reverse proxy that
terminates TLS in front of it, and set `MCP_TRUSTED_PROXIES` to the proxy's
address so the server counts sign-in attempts per visitor rather than all as the
proxy. The server reads `X-Forwarded-For` only from the addresses listed there.

| Variable | Required | Meaning |
| --- | --- | --- |
| `HA_URL` | yes | Base URL of Home Assistant, e.g. `http://homeassistant:8123` |
| `HA_TOKEN` | yes | Long-lived access token of an admin user |
| `MCP_ORIGIN` | yes | Public base URL, e.g. `https://ha-mcp.example.com` |
| `MCP_SIGNING_KEY` | yes | Signs the OAuth credentials. At least 32 characters of random data, e.g. from `openssl rand -base64 32` |
| `MCP_USERNAME` | password mode | What you type on the sign-in page |
| `MCP_PASSWORD` | password mode | The password for it, at least 12 characters |
| `MCP_TRUSTED_PROXIES` | no | Comma-separated addresses or CIDRs of reverse proxies whose `X-Forwarded-For` is used, e.g. `172.16.0.0/12` |
| `MCP_ADDR` | no | Listen address, `:8080` by default |
| `MCP_READ_ONLY` | no | `true` to offer only the read tools |
| `MCP_OIDC_ISSUER` | OIDC mode | Provider base URL, e.g. `https://id.example.com` |
| `MCP_OIDC_CLIENT_ID` | OIDC mode | Client id registered at the provider |
| `MCP_OIDC_CLIENT_SECRET` | no | Only for confidential clients; omit for a public PKCE client |
| `MCP_OIDC_PROVIDER_NAME` | no | Name shown on the sign-in button, e.g. `Pocket ID` |
| `MCP_OIDC_SCOPES` | no | Scopes to request, `openid email profile` by default |
| `MCP_OIDC_REDIRECT_URI` | no | Overrides the callback, `$MCP_ORIGIN/oidc/callback` by default |
| `MCP_OIDC_ALLOWED_EMAILS` | OIDC mode¹ | Comma-separated verified addresses allowed to sign in |
| `MCP_OIDC_ALLOWED_SUBJECTS` | OIDC mode¹ | Comma-separated provider subject ids allowed to sign in |
| `MCP_OIDC_ALLOW_ANY` | OIDC mode¹ | `true` to accept every account the provider authenticates |

¹ OIDC mode needs at least one of these three.

## Connecting an agent to it

Add `https://your-origin/mcp` as an MCP connector. The agent will send you to a
sign-in page. Enter the username and password from the container's environment,
or continue with your identity provider if one is configured.

There is no account to create and no other user interface. The sign-in page and
the OAuth endpoints behind it exist because the OAuth flow is the only way the
connector UIs know how to authenticate against an MCP server.

## Delegating sign-in to an identity provider

Instead of a password in the container's environment, the sign-in step can be
handed to any OpenID Connect provider. The flow is unchanged from the agent's
point of view, it still gets an authorization code from this server, but when
the browser reaches the sign-in page, the server sends it to your provider, and
only mints the code once the provider has authenticated the person and returned
a verified ID token.

This works with Pocket ID, Keycloak, Authentik, Auth0, Google, Entra ID and
anything else that publishes a `.well-known/openid-configuration`. Nothing is
tailored to a specific vendor: the server reads the provider's endpoints from
discovery and is configured with an issuer and a client id.

Set `MCP_OIDC_ISSUER` and `MCP_OIDC_CLIENT_ID` to turn it on, say who may sign
in (see [Who is allowed in](#who-is-allowed-in)), and drop
`MCP_USERNAME`/`MCP_PASSWORD`. When an issuer is set, sign-in goes through OIDC
and a leftover `MCP_USERNAME`/`MCP_PASSWORD` is ignored; the server logs a
warning saying so rather than pretending the password still works.

### Pocket ID

1. Create an OIDC client in Pocket ID under **Administration → OIDC Clients →
   Add OIDC client**.
   - **Name**: `mcp-for-homeassistant`
   - **Callback URL**: `https://ha-mcp.example.com/oidc/callback`, this must be
     `MCP_ORIGIN` plus `/oidc/callback` unless you override
     `MCP_OIDC_REDIRECT_URI`.
   - Leave **Public client** on to authenticate with PKCE and no secret, or turn
     it off and copy the secret.
2. Start the container with the client details:

   ```sh
   docker run -p 127.0.0.1:8080:8080 \
     -e HA_URL=http://homeassistant:8123 \
     -e HA_TOKEN=... \
     -e MCP_ORIGIN=https://ha-mcp.example.com \
     -e MCP_OIDC_ISSUER=https://id.example.com \
     -e MCP_OIDC_CLIENT_ID=<client id> \
     -e MCP_OIDC_PROVIDER_NAME='Pocket ID' \
     -e MCP_OIDC_ALLOWED_EMAILS=you@example.com \
     -e MCP_SIGNING_KEY="$(openssl rand -base64 32)" \
     ghcr.io/barmore-genc/mcp-for-homeassistant:latest
   ```

   `MCP_OIDC_ISSUER` is Pocket ID's own URL, the one you open in a browser, not
   an internal container address. `MCP_OIDC_CLIENT_SECRET` is only needed if you
   turned **Public client** off. To accept anyone who can sign in at Pocket ID,
   replace `MCP_OIDC_ALLOWED_EMAILS` with `MCP_OIDC_ALLOW_ANY=true`.

3. Connect the agent as usual. The sign-in page now shows a single **Continue
   with Pocket ID** button; after Pocket ID authenticates you, you land back on
   `/oidc/callback` and the agent receives its code.

### Other providers

The same variables apply. For Google, for example:

```sh
-e MCP_OIDC_ISSUER=https://accounts.google.com \
-e MCP_OIDC_CLIENT_ID=...apps.googleusercontent.com \
-e MCP_OIDC_CLIENT_SECRET=... \
-e MCP_OIDC_PROVIDER_NAME=Google \
-e MCP_OIDC_ALLOWED_EMAILS=you@gmail.com
```

Register `https://ha-mcp.example.com/oidc/callback` as the authorized redirect URI
at the provider. Request whatever scopes you need with `MCP_OIDC_SCOPES`; the
`openid` scope is always present.

### Who is allowed in

Set `MCP_OIDC_ALLOWED_EMAILS` and/or `MCP_OIDC_ALLOWED_SUBJECTS` to pin the
server to specific identities. An email has to match exactly, case-insensitively,
and only counts when the provider marks it as verified (`email_verified`); the
subject is the provider's stable user id. A sign-in that matches neither is sent
back to the agent as `access_denied` and logged by the server.

`MCP_OIDC_ALLOW_ANY=true` accepts every account the provider authenticates
instead. Use it only when the provider itself contains nobody but you: with
Google, for example, it would let in anyone with a Google account. The server
refuses to start in OIDC mode unless one of these is set.

## How authentication works

The container is its own OAuth 2.1 authorization server. It registers clients
dynamically, requires PKCE with S256, and issues an access token good for an
hour and a refresh token good for thirty days. Each refresh extends that, up to
90 days after you signed in; after that the agent sends you through the sign-in
page again.

None of that is stored. Every credential it issues is a signed string that
carries its own contents, so the container keeps no database and no volume, and
restarting or replacing it does not disconnect anything. The only thing held in
memory is the set of authorization codes already redeemed, which expire a minute
after they are minted.

Changing `MCP_SIGNING_KEY` invalidates every outstanding token at once, which is
how you revoke access. Changing `MCP_PASSWORD` only affects future sign-ins.

The sign-in page shows the address the agent will receive access at. Check it
before you sign in: the app name shown below it is whatever the app registered
itself as. Sign-in attempts are limited to 5 per minute per address and 20 per
minute in total.

With an identity provider, a sign-in has to finish in the browser that started
it. The signed state that carries it through the provider expires after ten
minutes and can be used only once.

## Tools

Tools marked *write* change Home Assistant. They are not offered when
`MCP_READ_ONLY` is `true`.

Entities and states:

- `ha_list_entities`: find entities and their current state, filtered by domain, area, label, device or search text.
- `ha_get_state`: all attributes and registry details of one or more entities.
- `ha_history`: how entity states changed over a time range, with min, max and average for sensors.
- `ha_logbook`: what happened and which automation, script or user caused it.
- `ha_statistics`: long-term statistics per hour, day, week or month, such as daily energy use.
- `ha_render_template`: render a Jinja template the way an automation would. A template can read a camera's access token, which gives view access to that camera for about 10 minutes.
- `ha_camera_snapshot`: a current image from a camera.

Services and events:

- `ha_list_services`: the services Home Assistant offers, with their fields and descriptions.
- `ha_call_service` (*write*): call a service, such as `light.turn_on`.
- `ha_listen_events`: listen on the event bus for a few seconds and return what fired. At most 8 listens run at once.
- `ha_fire_event` (*write*): fire a custom event.

Automations, scripts and scenes:

- `ha_get_automation`: list automations, scripts or scenes, or read one's config as YAML.
- `ha_manage_automation` (*write*): save, delete, enable, disable, trigger, run or activate one.
- `ha_validate_config`: check triggers, conditions and actions without saving.
- `ha_traces`: step-by-step records of recent automation and script runs.
- `ha_list_device_automations`: the device triggers, conditions and actions a device offers.
- `ha_find_related`: everything connected to an entity, device, area, automation or blueprint.
- `ha_list_blueprints`: installed blueprints and their inputs.
- `ha_manage_blueprint` (*write*): import, write or delete a blueprint. Blueprints with YAML tags other than `!input`, such as `!include` or `!secret`, are refused. Imports only accept public hosts, but Home Assistant downloads the URL itself and follows redirects, so where a redirect leads is not checked.

Areas, devices, helpers and integrations:

- `ha_list_registry`: areas, floors, labels, categories, devices, entities, persons and zones.
- `ha_manage_registry` (*write*): create, update or delete areas, floors, labels, categories and zones, and update devices and entities.
- `ha_list_helpers`: the helpers created in the UI with their settings.
- `ha_manage_helper` (*write*): create, update or delete a helper.
- `ha_list_integrations`: configured integrations and their state.
- `ha_manage_integration` (*write*): reload, enable or disable an integration.

Dashboards:

- `ha_get_dashboard`: list dashboards, or read one's config as YAML.
- `ha_save_dashboard` (*write*): replace a dashboard's config.

Calendars, to-do lists and notifications:

- `ha_calendar_events`: list calendars, or the events of one.
- `ha_list_todo_items`: list to-do lists, or the items of one.
- `ha_list_notifications`: the notifications shown in the sidebar.

System and backups:

- `ha_system_log`: logged warnings and errors, or the end of `home-assistant.log`.
- `ha_check_config`: run the configuration check on the YAML files.
- `ha_restart` (*write*): restart Home Assistant after a passing configuration check.
- `ha_backup_info`: the backups, when the next automatic backup runs and the automatic backup settings.
- `ha_create_backup` (*write*): start a backup with the automatic backup settings.

## Development

Build and run the unit tests:

```sh
go build ./...
go vet ./...
go test -race ./...
```

The integration tests run against a real Home Assistant. `dev/ha-test/setup.sh`
starts one in Docker as the container `ha-mcp-test` on port 18124 (set
`HA_TEST_PORT` for another port), onboards it with the demo integration, keeps
its configuration in `dev/ha-test/config` and writes its URL and an admin token
to `dev/ha-test/env`. Running it again reuses the container and the token.

```sh
dev/ha-test/setup.sh
env $(cat dev/ha-test/env) go test -tags integration ./...
```

The integration tests create and remove their own automations, scripts,
scenes, helpers, dashboards and backups, overwrite the default dashboard and the
backup settings, and `TestIntegrationZRestart` restarts Home Assistant. Run them
against the test instance only. Add `-skip TestIntegrationZRestart` to keep it
running.

`dev/e2e/run.sh` walks the sign-in pages in Chromium through Playwright's Docker
image: password sign-in through to an `/mcp` call, a wrong password, Cancel, and
the redirect to an OIDC provider. It checks that the browser applies the pages'
Content-Security-Policy without blocking any of these steps. It uses the test
instance from `dev/ha-test/env`.

```sh
dev/e2e/run.sh
```
