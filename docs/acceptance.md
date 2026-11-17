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

Use `task check` for race, SDK conformance and vet checks, `task smoke` for live
monitoring, and `task acceptance` for the additional live API checks. Do not run
hardware acceptance of physical controls, upload or print start without
explicit operator approval and a safe printer state.
