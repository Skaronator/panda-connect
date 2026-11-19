# H3C acceptance record

The local Compose deployment was rebuilt and recreated against H3C firmware
`01.01.03.00`. These checks did not interrupt the existing print or change
printer settings.

| Area | Result | Still unverified on hardware |
| --- | --- | --- |
| Monitoring | UI assets, connected device, running job, temperatures and full status refresh passed. | Other firmware revisions. |
| Controls | Live API returned the expected temperature descriptors and rejected missing, out-of-range and malformed values. | Valid heating, pause, resume and stop. |
| Files/printing | Live API reported `sdcard` after an authenticated FTPS probe and listed four root entries. | Upload and approved single-plate print start; both are disabled on this deployment. |
| PrintTool/AMS | The host showed nine enabled home IDs and nine AMS 3H slot-presence fields, six occupied at inspection, consistent with the hardware topology. | Tool changes and acceptance of print dispatch mappings; transition and refusal tests use fixtures/fakes. |
| Camera | Configured through the camera API. A WebRTC client negotiated ICE, received H.264 RTP and decoded frames. There were no remaining established printer camera sockets after the viewer closed. | The local Compose host has no login, so multi-user authorization was exercised by the released host's camera and authorization tests, not on the live deployment. |

A later read-only probe during a print reported the nominal build volume,
the primary light on, and head 0 linked to the first AMS slot with PLA. Panda
did not publish a chamber target in that probe. The probe did not change panel
settings or routing.

Use `task check` for race, SDK conformance and vet checks, `task smoke` for live
monitoring, and `task acceptance` for the additional live API checks. Do not run
hardware acceptance of physical controls, upload or print start without
explicit operator approval and a safe printer state.

## Supervised checks

1. Run `task acceptance` before enabling uploads or dispatch. Compare the
   reported head links and slot presence with the printer's panel. If the panel
   shows a chamber target or light state, compare it with `task smoke` before
   and after changing that setting from the panel. Record the firmware and
   whether the setting appeared in an update or only after a status refresh.
2. With an operator at the printer, use a small test print to check pause and
   resume. Check both the panel and the host job history. Check stop on a
   separate test print. If a command times out, check the panel before retrying.
3. Test upload to a fresh filename with no other client writing to the same
   directory. Verify the file's size and digest independently of the upload
   result. Test an approved single-plate start separately using the
   [manifest workflow](protocol.md#experimental-print-dispatch). Record the
   physical feeder slot and PrintTool the printer used, not just the command
   reply. Disable experimental options when finished.

Record successful heater tests separately, with the target chosen by the local
operator for the loaded material and build plate. Do not use the limits in the
API descriptors as test setpoints.
