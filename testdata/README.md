# H3C telemetry fixtures

Reduced from read-only reports observed on an H3C running 01.01.03.00.
Printer and accessory serials and the job name are replaced. Network details,
spool identifiers, camera URLs and unused fields are omitted. No access code
is recorded. These fixtures retain the original field types and packed values.

They cover the initial monitoring subset, not the complete H3C protocol.

## A1 mini

`a1-mini-version.json` and `a1-mini-idle.json` are reduced from the public
[bambu-rs A1 mini captures][source] (`01.07.02.00`, LAN mode, AMS Lite class
attached). The version fixture substitutes a test serial. The idle fixture
keeps only scalar monitoring fields and omits network, accessory and camera
data. Neither fixture was collected by PandaLink.

The `a1-mini-*.synthetic.json` files are synthetic running and paused reports
for parser and reconnect coverage. They make no claim about real running job
IDs, pause deltas or error values on this firmware.

[source]: https://github.com/sksat/bambu-rs/tree/ad3da2b6e86bae853eafc51d4fb487dc0efebf87/tests/fixtures

## P2S

`p2s-version.json` reduces the [public P2S version reply][p2s], with test
serials. `p2s-idle.synthetic.json` is a synthetic snapshot based on the notes
describing a `msg`-less reply and nested temperatures. It is not a hardware
capture.

[p2s]: https://github.com/ClusterM/open-bamboo-networking/blob/fcba35db1036a3dd09dcae40c84a7bb792c48c8a/research/12.01-status.md
