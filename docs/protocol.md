# H3C controls, storage and print dispatch

## Controls

The generic host endpoint is `POST /api/v1/devices/{id}/commands`. Its `verb`
is prefixed with `core.command.` and its `args` values are strings. Fetch
`GET /api/v1/devices/{id}/capabilities` for the argument descriptors.

| Verb suffix | Arguments or required observed state |
| --- | --- |
| `send_state_info` | No arguments; read-only MQTT full status request |
| `pause_print` | `PRINTING` |
| `resume_print` | `PAUSED` |
| `stop_print` | `PRINTING`, `PAUSED` or `BUSY` |
| `set_nozzle_temperature` | `nozzle_temperature`: integer 0–350 °C for the active nozzle |
| `set_bed_temperature` | `bed_temperature`: integer 0–120 °C |

The [H3C FAQ](https://wiki.bambulab.com/en/h3c/manual/faq) specifies the machine
limits, not safe settings for every filament or build plate. Temperature
commands emit only fixed `M104` or `M140` with a bounded integer, not arbitrary
G-code. An MQTT publish is not acceptance. The driver waits for a reply with
the matching command and sequence; pause/resume and heating may also be
confirmed by a new matching telemetry value. That fallback observes state,
not causation. A timeout or disconnect after publish is ambiguous: inspect
the printer before retrying. Stop and print-start require a correlated reply.
Controls beyond safe argument rejections have been tested only with a fake.

## Storage

Read `GET /api/v1/devices/{id}/storages?refresh=true` to confirm the FTPS root
is readable. `reported` means the current session has successfully listed it,
not that capacity, free space or removable media were reported. Without
`refresh`, the host returns held data. Browse with
`GET /api/v1/devices/{id}/files?path=sdcard&refresh=true`. If it returns
`reading: true`, read again without `refresh`. There is no file-change feed
for writes by other clients; explicitly refresh to see them.

FTPS uses implicit TLS 1.2, a verified pin or CA, `PROT P`, passive connections
to the control peer, and TLS session reuse. Listing ignores symlinks and checks
ancestor directories before traversing them. Panda Connect exposes no deletion,
folder creation, or download API. Upload requires `allow_uploads: "true"` in
endpoint options. It streams at most 2 GiB and checks the final name and size.
`LIST` followed by `STOR` is **not atomic**: another client can create a file
after preflight. Enable uploads only with exclusive control of the destination
directory, use fresh filenames, and inspect the result after any failure. An
interrupted upload can leave a partial file. Hardware upload is unverified.

## PrintTool and AMS inventory

Heads `0`–`8` are PrintTool home `addr_id` values, not the changing mounted/rack
wire IDs. `/bambu/printtools/{home}` keeps available `connected`, `installed`,
`mounted`, `type`, and `source` information. The SDK cannot represent a shared
feeder graph: a known `ams:32:{slot}` source appears as one feeder child
`{home}.0` under the corresponding head, not as the physical slot number.
Read `/bambu/ams/32/slots/{slot}` and `/bambu/ams/32/zones/{zone}` for physical
presence, material and zone readings. Missing means unknown, not zero or empty;
a reported clear presence bit means empty. Deltas retain omitted fields and
full reports replace the inventory. Conflicting mounted indications produce
no unique `/bambu/active_tool`; missing source links are never inferred from
head order. Mounted temperatures come from packed extruder telemetry when
available, not the legacy scalar for a possibly racked nozzle.

## Experimental print dispatch

Leave `print_manifest_file` unset unless testing this workflow with an operator
at an idle printer. The manifest is a read-only trusted file inside the server
container, loaded on connection. Never generate approvals from untrusted project
metadata. Example (replace the size and digest with the actual values):

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

Only root-level, already-sliced `.gcode.3mf` files with a simple ASCII basename
are accepted. `single_plate` attests that the archive has only plate 1 and that
`Metadata/plate_1.gcode` is the intended slice; Panda Connect does not open, validate,
or slice projects. List every used slicer filament in dense mapping order
starting at index zero; unused/sparse indices are unsupported. The manifest
also attests that the file, build plate and calibration are appropriate. The
driver sends optional calibration, inspection and timelapse flags off. Do
not assume this overrides actions baked into the G-code or required by firmware.

Supported sources are reported AMS 3H unit 32 slots 0–8 explicitly linked to
installed `HS01` or `HH01` 1.75 mm PrintTools. Material must match the reported
type exactly (`PLA`, `PETG`, `ABS`, `ASA`, `PVA`, `PC`, `PA`), and the reported
nozzle diameter must match the slice. No separate guarantee of filament or
temperature compatibility is inferred from the label. External spools, HT01,
duplicate sources and missing/ambiguous telemetry are unsupported. The host
`/print` route accepts a path but no plate or material options; use the generic
`core.command.start_print` command for an optional `tool_mapping` argument
(a JSON array encoded as a string, such as `["0.0"]`). The stock UI is not an
H3C slicer or mapping editor.

Dispatch checks size and streams a SHA-256 over FTPS, requires a fresh full MQTT
report, and rechecks inventory and state. It builds `project_file` with a
printer-local `ftp://<basename>` reference and both AMS encodings (`28+slot`
and `{ams_id:32, slot_id:slot}`). These encodings follow BambuStudio behavior as
documented by [Bambuddy’s H3C research](https://github.com/maziggy/bambuddy/issues/3442).
The command requests material routing; it does not force the firmware's rack
nozzle selection with a separate `nozzle_mapping` field. The complete wire
command is fake-tested only. Do not change the file, AMS routing or printer
state concurrently: verification and printer start cannot be made atomic
across FTPS, MQTT and the printer's own controls.
