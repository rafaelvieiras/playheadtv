# Deploying the backend

`compose.example.yml` is a Docker Compose project for running the Playhead TV
backend (REST API, MCP, embedded admin web UI) in production. It is meant to be
synced from a Git repository ("GitOps") by a Compose-aware manager: the
repository holds the compose file, and the image tag inside it is the single
thing that says which release runs.

It is an example with placeholders. Copy it into your own deployment
repository; do not point a manager at this repository's root
`docker-compose.yml` (that file builds from source and seeds a development-only
admin).

## What you get

- The image `ghcr.io/rafaelvieiras/playheadtv-backend`, published by the
  [backend release workflow](../.github/workflows/backend-release.yml) for
  `linux/amd64` and `linux/arm64`, with immutable tags `vX.Y.Z` and
  `sha-<commit>`. There is no `latest` tag.
- A named volume `playheadtv-prod-data` holding the SQLite database and
  `encryption.key`.
- A healthcheck that runs `/server healthcheck` (the image has no shell or
  curl) against `GET /healthz`.
- `GET /version` (public, no credentials) returning
  `{"version": "1.2.3", "api_version": 1, "commit": "..."}`: the release that is
  running and the integer API version the clients check against. A build from
  source reports `"dev"`.

## First deployment

1. Copy `compose.example.yml` into your deployment repository and set the image
   tag to a published backend release (see the repository's releases; backend
   releases are tagged `vX.Y.Z`, client releases `client-vX.Y.Z`).
2. Review the environment block. `PLAYHEADTV_EPG_WINDOW` is the main knob; the
   rest of the backend's variables are in the root `README.md`. Decide how the
   encryption key is handled (next section).
3. Let your manager deploy it and wait for the container to become healthy.
   Check `curl http://127.0.0.1:9394/version` on the host.
4. Create the admin user **before** exposing the instance (next section).

### Encryption key

Stored media-source credentials are encrypted at rest. Either:

- do nothing: on first start the backend generates a key into the data volume
  (`encryption.key`, mode 0600). It is then part of every backup, and a backup
  of the database without it cannot decrypt credentials; or
- provide `PLAYHEADTV_ENCRYPTION_KEY` (32 random bytes, base64:
  `openssl rand -base64 32`) from your manager's secret store. Never commit it
  to the repository. Keep it with your other secrets: losing it loses the
  stored credentials.

### Creating the admin user (no seeded admin)

The compose project never sets `PLAYHEADTV_SEED_ADMIN_EMAIL` or
`PLAYHEADTV_SEED_ADMIN_PASSWORD`, so a fresh instance has no user. The backend
has exactly one app user per instance, created through the public
`POST /app-users` endpoint. It succeeds only while no user exists (afterwards
it answers `409`), and the password must be at least 8 characters. Because the
endpoint is public until that first call, do it before the instance is reachable
from outside, from the host (the example publishes the port on loopback only):

```sh
curl -sS -X POST http://127.0.0.1:9394/app-users \
  -H 'Content-Type: application/json' \
  -d '{"email": "admin@example.com", "password": "<a strong password>"}'
```

Use your own address and password (not these placeholders; the reply echoes the
user without the password). Then open `https://<your host>/admin/` and sign in.
If the password is lost, see "Recovering a lost admin password" below.

### Recovering a lost admin password

Run the reset command on the server, inside the running container (the image has
no shell; the command is part of the server binary):

```sh
docker exec <container> /server reset-password
```

It prints a single-use token (valid 15 minutes; `--ttl 30m` changes that, up to
1 hour) and the page that accepts it. Open
`https://<your host>/admin/reset-password#token=<token>` (or open the page and
paste the token), choose the new password twice and sign in. Notes:

- It works while the server is running; it talks to the same database file
  through `PLAYHEADTV_DB_PATH` (set in the image).
- `--email <address>` selects the user; it is only needed with several users.
- Running it again replaces the pending token. Five wrong attempts also burn it.
- Resetting ends every web UI and MCP session. Paired playback clients keep
  working: device tokens are not touched.
- Anyone who can run commands in the container can do this, which is why no
  other credential is needed.

### Pairing a client

In the web UI, open Devices and create one for the client machine (platform
`linux`). The token is shown once. Put it, with the backend URL, into the
client's `config.json` (`playhead_base_url`, `playhead_device_token`; see
`client/README.md`).

## Upgrading

The supported flow is one commit in your deployment repository:

1. Read the release notes of the new backend release. They state whether it
   carries a database migration.
2. If it does, take a backup first (next section).
3. Change the image tag in the compose file to the new `vX.Y.Z` and commit.
4. Your manager syncs the change and recreates the container. On start the
   backend applies pending migrations and backfills **before** it listens, so
   the healthcheck stays failing (within `start_period`, 120 s here) until they
   are done. Nothing else is needed.
5. Check `GET /version` reports the new version.

Clients are independent: a backend upgrade does not require a client upgrade as
long as the backend's `api_version` is inside the range the client supports. A
client outside that range tells the user so on start (it does not crash), and
installed clients update themselves from the client releases.

Migrations are forward-only and are not wrapped in one transaction, so a
migration that fails midway can leave a partial schema, and an older image
started on a migrated database is not supported. That is why rollback below is
a restore, not just a tag change.

## Backup before an upgrade with a migration

SQLite runs in WAL mode, so copy the volume while the backend is stopped. On the
host that runs the project (stop the service first, so the manager does not
restart it mid-copy, or pause syncing in the manager):

```sh
docker compose stop backend            # run in the project directory
docker run --rm \
  -v playheadtv-prod-data:/data:ro \
  -v "$PWD":/backup \
  busybox tar czf /backup/playheadtv-data-$(date +%F).tar.gz -C /data .
docker compose start backend
```

The archive holds the database (with its `-wal`/`-shm` files) and
`encryption.key`: store it somewhere safe and private, like any secret.

## Rolling back

1. Stop the backend (`docker compose stop backend`).
2. Restore the backup taken before the upgrade into the volume:

   ```sh
   docker run --rm \
     -v playheadtv-prod-data:/data \
     -v "$PWD":/backup \
     busybox sh -c 'rm -rf /data/* && tar xzf /backup/playheadtv-data-<date>.tar.gz -C /data'
   ```

3. Revert the tag commit in your deployment repository (back to the previous
   `vX.Y.Z`), so the manager and the volume agree.
4. Start the backend (or let the manager sync) and check `GET /version`.

For a release without a migration, step 2 is unnecessary: reverting the tag is
enough (the schema did not change). Data created since the upgrade is lost by a
restore; that is the price of a forward-only schema.

## Behind a reverse proxy

Terminate TLS in front of port 9394 and forward everything (`/`, `/admin/`,
`/mcp`). The client expects `playhead_base_url` to be the public HTTPS origin.
The update check of the client does not go through your backend: installed
clients read the client releases from GitHub over HTTPS.
