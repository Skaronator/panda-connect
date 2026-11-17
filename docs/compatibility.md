# Bambu Lab compatibility

## PandaLink verification

| Model | Firmware | Monitoring | Inventory | Controls | Storage listing | Upload | Print start | Camera |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| H3C | `01.01.03.00` | hardware-verified | hardware-verified | fake-tested | hardware-verified | fake-tested | fake-tested | hardware-verified |
| A1 mini | `01.07.02.00` (fixture source) | fake-tested | unavailable | unavailable | unavailable | unavailable | unavailable | unavailable |
| P2S | `01.02.00.00` (identity fixture source) | fake-tested | unavailable | unavailable | unavailable | unavailable | unavailable | unavailable |

These ratings apply to PandaLink, not to the printer's own capabilities.
`unavailable` means there is no enabled PandaLink implementation for that
model. Other catalog profiles have synthetic handshake coverage but no
model-specific telemetry evidence, so monitoring is `unknown`. Optional features
are unavailable for every non-H3C profile. For H3C, the
[acceptance record](acceptance.md) distinguishes observed inventory from tool
changes and live API checks from execution of physical commands.

A1 mini, P2S, and H3C fakes were connected to the pinned Prusa Connect server
on the same `pandalink` connector. The two read-only devices advertised only
status refresh, and the host refused a heater command for each before it could
reach the printer. H3C still advertised its heater control.

## Model inventory

This is the union of the [US comparison catalog][catalog] and the base machine
presets at the [audited Studio revision][presets]. It includes older models no
longer listed in that catalog and the separate H2D Pro preset. Availability
varies by region. Combo, laser and accessory bundles are not separate MQTT
identities unless a printer report shows otherwise. `—` means no MQTT identity
has been reviewed for this table; the profile tries the exact `Bambu Lab`
marketing name in that case. This is not a complete regional inventory.

| Marketing model | Studio preset `model_id` | Observed MQTT `ota.product_name` |
| --- | --- | --- |
| A1 mini | `N1` | `Bambu Lab A1 mini` ([public capture][a1]) |
| A1 | `N2S` | — |
| A2L | `N9` | — |
| P1P | `C11` | — |
| P1S | `C12` | — |
| P2S | `N7` | `Bambu Lab P2S` ([public capture][p2s]) |
| X1 | `BL-P002` | — |
| X1 Carbon (X1C) | `BL-P001` | — |
| X1E | `C13` | — |
| X2D | `N6` | — |
| H2S | `O1S` | — |
| H2D | `O1D` | — |
| H2D Pro | `O1E` | — |
| H2C | `O1C2` | — |
| H3C | `N10` | `Bambu Lab H3C` |

## Test a printer

1. Enable LAN-only Mode and follow the [MQTT certificate enrollment and access
   code setup](../README.md#run-locally-with-docker-compose). Public observations
   for `01.07.02.00` indicate Developer Mode is not necessary for reads. Leave
   it off unless you need it for another purpose.
2. Use `pandalink` for every model. For an existing deployment, create the
   endpoint through the host endpoint API using the printer's own serial,
   access code file and TLS pin. If you already configured an endpoint on
   `pandalink-monitor`, switch it to `pandalink` and remove the old connector
   instance from the server configuration.
3. Run `python3 scripts/smoke.py --require-model P2S`, replacing `P2S` with your
   model from the table. For non-H3C printers it checks that both device API
   responses advertise only `core.command.send_state_info`.
4. Compare the dashboard with the printer while idle and during an existing
   print. Observe a paused job if one is already available. Reconnect during
   a print by restarting the connector or server, and verify that the device ID
   and any reported job ID stay the same. Check that controls and files are not
   offered and leave the camera unconfigured. If an H3C is on the same server,
   confirm it still refreshes while the other printer reconnects.

Read-only profiles report state, nozzle and bed readings, and error codes when
provided. They do not invent job IDs. A scalar nozzle reading on a multi-nozzle
printer is not assigned to a particular tool. If a model is refused, share a
sanitized version reply: older firmware may report a different identifier or
none at all.

If you have an A1 mini or another candidate, share the model, firmware, LAN
settings, attached accessories and which checks passed. The smoke output and
`pandalink-probe` output are for local inspection: remove device and job IDs,
printer or accessory serials, private addresses, access codes and job names
before sharing logs or fixtures. Repeat the smoke check after firmware updates.
Do not start, pause, or otherwise change a print just to collect telemetry
without explicit operator approval.

[catalog]: https://bambulab.com/en-us/compare
[presets]: https://github.com/bambulab/BambuStudio/tree/8e524a39d4c4cd0541801c1c46d2f77debb0bf1d/resources/profiles/BBL/machine
[a1]: https://github.com/sksat/bambu-rs/blob/ad3da2b6e86bae853eafc51d4fb487dc0efebf87/tests/fixtures/get_version-a1mini.json
[p2s]: https://github.com/ClusterM/open-bamboo-networking/blob/fcba35db1036a3dd09dcae40c84a7bb792c48c8a/research/12.01-status.md
