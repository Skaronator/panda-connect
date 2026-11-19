<h1 align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset=".github/assets/panda-connect-dark.svg">
    <source media="(prefers-color-scheme: light)" srcset=".github/assets/panda-connect-light.svg">
    <img alt="Panda Connect" src=".github/assets/panda-connect-light.svg" width="432" height="104">
  </picture>
</h1>

Panda Connect is a local Bambu Lab printer driver for the self-hosted
[Prusa Connect](https://github.com/prusa3d/prusa-connect) server.
It connects directly over MQTT with TLS. You don't need a Bambu account, a cloud
connection, the Bambu Connect app, or a local Prusa Connect checkout. The
`panda-connect` connector selects a profile from the printer's reported identity.

## Printer support

| Model | What we've verified |
| --- | --- |
| H3C (`01.01.03.00`) | Live monitoring, PrintTool and AMS 3H inventory, FTPS browsing, and H.264 video over WebRTC. The live API returned control descriptors and rejected invalid inputs. Valid controls, uploads, and print starts have only been tested with a printer fake. |
| A1 mini (`01.07.02.00`) and P2S (`01.02.00.00`) | Read-only monitoring tested with fakes. The firmware versions come from fixtures. |
| A1, A2L, P1P, P1S, X1, X1 Carbon, X1E, X2D, H2S, H2D, H2D Pro, H2C | Read-only profiles with synthetic handshake coverage. Monitoring is unverified. |

Read-only profiles report printer state, nozzle and bed readings, and error
codes when available. They expose only status refresh. Controls, inventory,
files, print dispatch, and video are H3C-only.

On the H3C you can also see nozzle diameter, chamber readings, job progress and
remaining time, layer counts, and HMS errors. The driver reports a nominal build
volume of [330 × 320 × 325 mm](https://bambulab.com/en-us/h3c/specs), plus chamber
target and light state when the printer reports them. The nine PrintTool home
IDs (`0` through `8`) are distinct from physical AMS slots. A head's child ID
`{home}.0` is a host tool ID; `ams:32:{slot}` identifies the physical slot.
Panda Connect does not infer a link when the printer omits it.

Bambu's LAN protocol is not a stable, officially supported third-party API.
Other firmware versions still need testing. The [Bambu Studio source at
`8e524a39d4c4`](https://github.com/bambulab/BambuStudio/tree/8e524a39d4c4cd0541801c1c46d2f77debb0bf1d)
is a useful reference, but machine presets don't establish MQTT identities or
which local features work on a given model.

## Run locally with Docker Compose

You need Docker with Compose v2 and internet access for the first build. Enable
**LAN-only Mode** on the printer. For H3C controls, also enable **Developer
Mode**. For read-only testing, start with Developer Mode off.

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
   go run ./cmd/panda-connect-probe \
     -address 192.168.1.100:8883 -inspect-certificate
   ```

   This command sends no credentials or MQTT requests. It displays the offered
   certificate, **without authenticating it**. Confirm that the address belongs
   to your printer, verify the fingerprint through a trusted connection, and put
   the displayed SHA-256 value in `.env` as `BAMBU_TLS_FINGERPRINT`.

   Without host Go, build the image first, then inspect the certificate:

   ```sh
   docker compose build
   docker compose run --rm --no-deps --entrypoint /usr/local/bin/panda-connect-probe \
     prusa-connect -address 192.168.1.100:8883 -inspect-certificate
   ```

4. Start the server:

   ```sh
   docker compose up --build -d --wait
   docker compose logs --tail=50 prusa-connect
   ```

5. Open **http://localhost:8081**. The printer is added automatically on the
   first start. Change `PANDA_CONNECT_PORT` in `.env` if the port is occupied.

**This is a local development setup without login.** The published port binds
to `127.0.0.1`, not the LAN. Do not expose it through a public port, tunnel or
reverse proxy without configuring Prusa Connect authentication. Containers on
the Compose network can also reach this development API.

### Data and configuration

The `panda-connect-data` volume holds the server configuration, authorization
database, printer identity, endpoints, and job history. Compose uses `.env` to
set up the initial printer on first start. To change a saved address, serial, or
TLS pin, use `/api/v1/connector-endpoints` instead of editing `.env`. See the
running server's `/docs` for the API schema and version headers.

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

### Upgrading from PandaLink

The renamed Compose project uses a new volume by default. To keep your saved
configuration and history, stop the old server with
`docker compose -p pandalink stop prusa-connect` (use your old project name if
you customized it). Create `compose.override.yaml` before starting the new server:

```yaml
volumes:
  panda-connect-data:
    external: true
    name: pandalink_pandalink-data
```

Use the actual volume name if it differs. Existing `pandalink` connector and
camera IDs still work. Rename `PANDALINK_PORT` to `PANDA_CONNECT_PORT` in `.env`
and `PANDALINK_URL` to `PANDA_CONNECT_URL` if you set it for the smoke check.

## Check the connection

Install [Task](https://taskfile.dev/installation/) for the commands below.

| Command | What it checks |
| --- | --- |
| `task probe` | Normalized telemetry from the running container |
| `task smoke` | Frontend assets, live state, and a read-only status refresh |
| `task acceptance` | On an H3C with an AMS 3H, also checks control descriptors, invalid input rejections, and FTPS browsing |

The smoke check needs Python 3. It defaults to `http://127.0.0.1:8081`; set
`PANDA_CONNECT_URL=http://127.0.0.1:PORT` if you changed the port.

For a read-only model, run `python3 scripts/smoke.py --require-model P2S`,
replacing `P2S` with your model. Compare the dashboard with the printer while
idle and during a print that's already running. Check that the device and any
reported job IDs survive a reconnect, and that controls and files aren't offered.
Repeat after firmware updates. If you share logs, remove device and job IDs,
serials, private addresses, access codes, and job names. Include the model,
firmware, LAN settings, and attached accessories.

An informational `no_capability_profile` issue is expected. Check
`GET /api/v1/devices/{id}/capabilities` for the commands and arguments actually
available to the device; the host's authorization verb list is broader.

## Driver endpoint options

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

`timeout` defaults to `15s` for the initial handshake and status refresh.
`stale_after` defaults to `2m` of telemetry silence. Prusa Connect handles
reconnection when the driver closes a lost or stale stream.

## H3C controls

Send `POST /api/v1/devices/{id}/commands` with a `core.command.` verb and string
values in `args`. Fetch the device capabilities first for argument descriptors.

| Verb suffix | Argument or state required |
| --- | --- |
| `send_state_info` | No arguments; requests a full status report |
| `pause_print` | `PRINTING` |
| `resume_print` | `PAUSED` |
| `stop_print` | `PRINTING`, `PAUSED`, or `BUSY` |
| `set_nozzle_temperature` | `nozzle_temperature`: integer from 0 to 350 °C for the active nozzle |
| `set_bed_temperature` | `bed_temperature`: integer from 0 to 120 °C |

These are [H3C machine limits](https://wiki.bambulab.com/en/h3c/manual/faq), not
recommended temperatures for your filament or build plate. If a command times
out or the printer disconnects before confirming it, check the panel before
retrying. Only test physical controls, uploads, or print starts with the
operator's approval and the printer in a safe state. Panda Connect has no axis
motion, chamber controls, or filament and drying controls.

## H3C storage and print dispatch

Use `GET /api/v1/devices/{id}/storages?refresh=true` to check the FTPS mount.
Browse with `GET /api/v1/devices/{id}/files?path=sdcard&refresh=true`. If the
response says `reading: true`, retry without `refresh`. Refresh again to see
changes made by other clients. There is no download, delete, or create-folder API.

Uploads and print dispatch are off by default and unverified on hardware.
Uploads require `allow_uploads: "true"` and are limited to 2 GiB. Use a fresh
filename and keep other clients from writing to the destination directory: the
existence check and upload are separate operations. A failed transfer can leave
a partial file; inspect it before trying again.

For supervised print testing, set `print_manifest_file` to a trusted read-only
file mounted inside the server. Each entry must approve an already-sliced,
root-level `.gcode.3mf` with a simple ASCII basename. Replace the sample size
and SHA-256 with the values from the actual file:

```json
{
  "prints": [{
    "path": "sdcard/fixture.gcode.3mf",
    "sha256": "0000000000000000000000000000000000000000000000000000000000000000",
    "size": 12345,
    "model": "H3C",
    "plate": 1,
    "single_plate": true,
    "filaments": [{"tool": "0.0", "material": "PLA", "nozzle_diameter": 0.4}]
  }]
}
```

The approval means the archive contains only plate 1, its intended slice is
`Metadata/plate_1.gcode`, and the file, build plate, and calibration are suitable.
Panda Connect checks the size and digest but does not inspect the slice. Don't
generate approvals from untrusted project metadata. List every used filament in
slicer order, starting at index zero with no gaps. Each source must be an AMS 3H
slot explicitly linked to an installed `HS01` or `HH01` 1.75 mm PrintTool. The
reported material and nozzle diameter must match. External spools and duplicate
sources are unsupported.

The host `/print` route takes a path. To override tool assignments, use
`core.command.start_print` with `tool_mapping` as a JSON array encoded in a
string, such as `["0.0"]`. The command requests material routing; firmware still
chooses the rack nozzle. Keep the file, routing, and printer state unchanged
through dispatch because verification and print start are separate operations.

## H3C camera

With LAN liveview available on the printer, get the host printer ID from
`GET /api/v1/devices`. Create the camera with `PUT /api/v1/cameras/h3c-live`,
`Content-Type: application/json`, and `If-None-Match: *`. Use an authorized
session or bearer token on hosts with login enabled.

```json
{
  "type": "panda-connect-rtsps",
  "config": {
    "printer": "<host printer ID>",
    "address": "192.168.1.100",
    "access_code_file": "/run/secrets/bambu_access_code",
    "tls_fingerprint": "<verified SHA-256 camera certificate fingerprint>"
  }
}
```

Enter the private IP with no scheme or port. The adapter uses RTSPS on port
322. Verify the camera certificate separately, even if it matches the MQTT
pin. You can use `ca_file` and optionally `tls_server_name` instead of a pin.
Keep the access code out of URLs and camera config. The adapter's internal
loopback relay is plaintext; isolate the container from untrusted processes.

The Compose image includes FFmpeg for HTTP live view. Select `h3c-live` on the
dashboard and press **Play**. The tile is blank before playback because the
adapter does not provide thumbnails. For H.264 over WebRTC, post a receive-only
SDP offer as `application/sdp` to `/api/v1/cameras/h3c-live/webrtc`. The client
must be able to reach the host's ICE candidates, including UDP ports. There is
no snapshot, audio, or H.265 conversion support. Existing `pandalink-rtsps`
and `pandalink-h3c` camera records still work.

## Development

Run `task check` for both Go modules or `task` to list available tasks. The Go
tests use TLS MQTT, FTPS, and RTSP fakes and never contact a printer.

[`testdata/`](testdata/) contains sanitized H3C captures, reduced public
[A1 mini reports](https://github.com/sksat/bambu-rs/tree/ad3da2b6e86bae853eafc51d4fb487dc0efebf87/tests/fixtures),
and a reduced public [P2S version reply](https://github.com/ClusterM/open-bamboo-networking/blob/fcba35db1036a3dd09dcae40c84a7bb792c48c8a/research/12.01-status.md).
Files ending in `.synthetic.json` are test inputs, not hardware captures.

Dependencies are pinned in [`go.mod`](go.mod) and [`server/go.mod`](server/go.mod).
If the Renovate GitHub app is installed, it groups server and SDK updates into a
single pull request.

Release Please uses [Conventional Commits](https://www.conventionalcommits.org/).
Merging a release PR publishes `ghcr.io/skaronator/panda-connect` for AMD64 and
ARM64 with the release tag and `latest`. PR builds do not publish images.

## License

Panda Connect's source is AGPL-3.0; see [LICENSE](LICENSE). Prusa Connect and its SDK
carry their upstream license terms. The Docker image combines both projects;
confirm the applicable distribution terms with the upstream maintainers before
publishing or redistributing it. This repository's license does not relicense
Prusa Connect.
