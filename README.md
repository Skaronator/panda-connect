# PandaLink

PandaLink is a local Bambu Lab printer driver for the self-hosted
[Prusa Connect](https://github.com/prusa3d/prusa-connect) server.
The `pandalink` connector selects a profile from the printer's reported identity.
H3C is hardware-verified; the other [listed models](docs/compatibility.md) are
enabled for read-only monitoring and still need hardware verification.

It connects directly to the printer over MQTT with TLS. No Bambu account,
cloud connection, Bambu Connect application or local Prusa Connect checkout is
needed. Enable **LAN-only Mode**. Enable **Developer Mode** for H3C controls;
for other models, start with the [read-only setup](docs/compatibility.md#test-a-printer).

## H3C features and verification

- H3C identification, including firmware and a stable serial-based identity.
- Live printer state, nozzle/bed/chamber temperatures, and nozzle diameter.
- Standard host state names (`PRINTING`, `PAUSED`, `FINISHED`) so job history
  stays current; the original vendor state is kept at `/bambu/gcode_state`.
- Job name, stable local task ID, progress and remaining time in seconds.
- Layer counts, print error code and HMS reports under `/bambu/*` state paths.
- A read-only status-refresh command that waits for a full printer report.
- Disconnect detection and host-managed reconnection, including mid-print.

- Read-only PrintTool rack and AMS 3H inventory, including nine logical heads,
  explicitly linked feeder slots, and vendor slot/zone telemetry.
- Live protected FTPS storage browsing. Opt-in uploads and manifest-gated print
  dispatch are experimental; neither has been tried on hardware.
- Pause/resume/stop and nozzle/bed target commands. Descriptors and safe input
  refusals were checked through the live API; actual controls were tested only
  with the printer fake.
- Optional printer-bound camera with H.264 over WebRTC and HTTP live view
  for dashboard tiles. The Compose image includes FFmpeg for HTTP live view.

See [controls, storage and print workflow](docs/protocol.md) and
[camera setup](docs/camera.md) for API details and limitations. PandaLink does
not move axes, manage filament or drying, or create/delete printer files or
directories. See the [acceptance record](docs/acceptance.md)
for the distinction between live hardware and fake-only checks, and the
[Bambu Studio source audit](docs/bambu-studio.md) for cross-model gaps. See the
[compatibility matrix](docs/compatibility.md) for model status and test instructions.

The protocol has been checked against H3C firmware **01.01.03.00**. Other
firmware versions need testing. Bambu's local protocol is not a stable,
officially supported third-party API.

## Run locally with Docker Compose

Requirements: Docker with Compose v2 and internet access for the first image
build. Go and Node.js are built inside the image. No sibling repository is read.

1. Copy the example environment and make a private directory for the access code:

   ```sh
   cp .env.example .env
   mkdir -p .secrets
   chmod 700 .secrets
   ```

2. Set `BAMBU_ADDRESS` and `BAMBU_SERIAL` in `.env`. Put the printer's access
   code on one line in `.secrets/access_code`. Do not put it in `.env`, a URL,
   Compose YAML or source code. Both `.env` and `.secrets/` are ignored by Git
   and excluded from the Docker build context.

   Compose mounts the file as a secret for UID 10001. On Linux, file-backed
   Compose secrets retain host file permissions: the file must be readable by
   that UID (for example mode `0644` inside the private `0700` directory).
   Do not use `0600` owned by a different UID, or the container cannot read it.

3. Enroll the TLS certificate from a trusted local network. With Go 1.26.11 or
   newer on the host:

   ```sh
   go run ./cmd/pandalink-probe \
     -address 192.168.1.100:8883 -inspect-certificate
   ```

   This command sends no credentials or MQTT requests. It displays the offered
   certificate, **without authenticating it**. Confirm that the address belongs
   to your printer, verify the fingerprint through a trusted connection, and put
   the displayed SHA-256 value in `.env` as `BAMBU_TLS_FINGERPRINT`.

   Without host Go, build the image first, then inspect the certificate:

   ```sh
   docker compose build
   docker compose run --rm --no-deps --entrypoint /usr/local/bin/pandalink-probe \
     prusa-connect -address 192.168.1.100:8883 -inspect-certificate
   ```

4. Start the server:

   ```sh
   docker compose up --build -d --wait
   docker compose logs --tail=50 prusa-connect
   ```

5. Open **http://localhost:8081**. The printer is added automatically on the
   first start. Change `PANDALINK_PORT` in `.env` if the port is occupied.

**This is a local development setup without login.** The published port binds
to `127.0.0.1`, not the LAN. Do not expose it through a public port, tunnel or
reverse proxy without configuring Prusa Connect authentication. Containers on
the Compose network can also reach this development API.

### Data and configuration

The `pandalink-data` named volume holds the server configuration, authorization
database, printer identity, endpoints and job history. It is writable by the
non-root server user; the rest of the container filesystem is read-only.

On the **first start only**, `pandalink-init` seeds `/data/config.yaml` and the
host's initial resource overlay from `.env`. Existing files are never replaced.
After that, change the printer endpoint through the Prusa Connect endpoint API
(`/api/v1/connector-endpoints`; see `/docs` for its schema and version headers).
Changing `BAMBU_ADDRESS`, `BAMBU_SERIAL` or `BAMBU_TLS_FINGERPRINT` in `.env`
does not overwrite a saved endpoint on subsequent starts.

To rotate an access code, update `.secrets/access_code` and force-recreate the
container so Docker rebinds the secret file:

```sh
docker compose up -d --force-recreate --wait
```

Stop without deleting records:

```sh
docker compose down
```

Do not add `--volumes` unless you intend to delete the deployment's saved data.

## Check the connection

Read normalized telemetry from the running container:

```sh
task probe
```

Check frontend assets, live device state and a read-only status refresh:

```sh
task smoke
```

The smoke check needs Python 3 and defaults to `http://127.0.0.1:8081`.
For another port, set `PANDALINK_URL=http://127.0.0.1:PORT`.
For a connected H3C with an AMS 3H, `task acceptance` also checks control
descriptors, invalid inputs that cannot publish printer controls, and FTPS
storage browsing.

An informational `no_capability_profile` issue is expected: PandaLink ships no
host firmware matrix. `/api/v1/devices/{id}/capabilities` shows the commands and
argument descriptors narrowed by the reported printer identity and endpoint
settings. The broader authorization verb list is not the capability list.

## Development

Install [Task](https://taskfile.dev/installation/) to run the commands below.
Run `task` to list the available tasks.

```sh
task check
go test -v -run 'Test(Contract|MonitorContract)' ./internal/bambu
task up
```

Normal Go tests never contact a printer. They run SDK conformance and protocol
tests against TLS MQTT, FTPS and RTSP fakes. `testdata/` contains reduced,
sanitized captures and labeled synthetic reports. `scripts/smoke.py` checks the live host without sending
physical controls.

### Pinned dependencies

- Driver SDK: `github.com/prusa3d/prusa-connect/sdk v0.1.0-beta.1`
  (Git tag **`sdk/v0.1.0-beta.1`**).
- Composed server: `github.com/prusa3d/prusa-connect v0.1.0-beta.1`.
- Both upstream tags resolve to commit
  `b194cb83b682822e95e839a3cecf4391f3929e87`.

`server/` is a separate Go module that imports the released server and this
repository's driver. Its only local replacement is PandaLink itself. Neither
the upstream server nor SDK is replaced with a local directory.

`internal/bambu/` owns the driver, sessions, and model capabilities.
`internal/telemetry/` parses reports, `internal/ftps/` handles file transport,
and `internal/mqttwire/` enforces MQTT frame limits.

The Dockerfile builds the upstream frontend from a checksum-pinned archive at
that same commit. It embeds the generated assets into the image's private Go
module cache before compiling `server/`; no host module cache is modified.
It does not build the optional first-party marketplace modules.

### Driver endpoint options

| Option | Meaning |
| --- | --- |
| `serial` | Required printer serial; checked against the identity reply |
| `access_code_file` | Required path to the secret file, read on each connection |
| `tls_fingerprint` | SHA-256 of the exact DER printer certificate, hexadecimal |
| `ca_file` | Alternative to pinning: a PEM CA bundle with normal TLS verification |
| `tls_server_name` | Optional name to verify when using a CA bundle |
| `ftps_port` | Implicit FTPS port, default `990` on the MQTT endpoint host |
| `allow_uploads` | `true` opts in to experimental uploads; default `false` |
| `print_manifest_file` | Absolute path in the server to a trusted approval manifest; unset disables dispatch |

Endpoint addresses accept a host or `host:port` (default `8883`), never
credentials or URL schemes. TLS is restricted to 1.2 for printer compatibility;
there is no unverified connection option. A certificate change requires
verification and a new endpoint pin.

Connector settings `timeout` (default `15s`) and `stale_after` (default `2m`)
bound the initial handshake/refresh and telemetry silence respectively.
PandaLink closes a lost or stale state stream; Prusa Connect then reconnects
with its own backoff. Paho's separate automatic reconnect loop is disabled.
Slow consumers keep the newest pending value per known state path, including
fields sent only once; replaced pending patches are counted in link telemetry.

## License

PandaLink's source is AGPL-3.0; see [LICENSE](LICENSE). Prusa Connect and its SDK
carry their upstream license terms. The Docker image combines both projects;
confirm the applicable distribution terms with the upstream maintainers before
publishing or redistributing it. This repository's license does not relicense
Prusa Connect.
