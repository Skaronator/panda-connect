# Bambu Studio source audit

Reviewed Bambu Lab’s public Bambu Studio source at
[`8e524a39d4c4`](https://github.com/bambulab/BambuStudio/tree/8e524a39d4c4cd0541801c1c46d2f77debb0bf1d).
This is a reference for how Studio behaves, not a published LAN API contract.
The [machine presets][presets] cover A, P, X and H families, but preset names
and IDs do not determine exact MQTT identity, supported transports, or firmware
behavior. See the [compatibility matrix](compatibility.md) for the H3C and
experimental A1 mini profiles and their verification status.

| Area | Evidence from Studio | Gap in PandaLink |
| --- | --- | --- |
| Identity and telemetry | [DeviceManager][identity] reads module `product_name` and firmware data. It uses `get_version` and `pushall`. Supported operations can also depend on reported feature flags. | Collect sanitized version, full status and delta fixtures per family and firmware. Verify exact product identification and which telemetry can be shared before enabling read-only sessions. |
| Thermal and job controls | [DeviceManager][controls] sends pause/resume/stop with `param: ""`; resume also checks a condition. Studio can send `set_bed_temp` when the printer advertises bit 39; otherwise it uses `M140`. It has both `M104` and nozzle-ID-aware `set_nozzle_temp` paths. | PandaLink uses the H3C limits and active-nozzle `M104`/`M140` workflow. It has not verified native temperature commands, nozzle routing, Studio’s resume conditions, or reply handling on other families. Studio’s job-control envelope includes a field PandaLink currently omits; test the difference before changing hardware behavior. |
| Tool and feeder topology | [DevNozzleSystem][nozzles] reads home `addr_id`, packed temperatures and feeder source `snow`. [SelectMachine][mapping] carries both AMS mapping forms and conditionally includes a separate nozzle mapping. | The nine-home-ID rack and AMS 3H mapping belong to H3C. Legacy AMS, different rack sizes and multi-nozzle routing need distinct fixtures and mapping rules. The current print workflow does not send a separate `nozzle_mapping`. |
| Storage and print | [MediaFilePanel][storage] checks internal-storage support separately. [PrintJob][printjob] passes mappings and selects among local send, local print and SD-card print. [NetworkAgent][plugin] resolves the actual print operations from a dynamically loaded network module. | Do not infer a single `sdcard` mount, FTPS behavior, or `project_file` payload from Studio’s UI. Verify available storage and protected listings first, then exact upload and print-start behavior separately. The public wrapper does not expose the complete wire payload for the plugin print paths. |
| Live video | [DeviceManager][camera-telemetry] reads `ipcam` liveview data, and [MediaPlayCtrl][camera] chooses between Bambu local transport on port 6000, RTSPS, and RTSP. | `pandalink-rtsps` supports the verified H.264 TLS path only. Port 6000 transport and other camera variants need their own adapters and tests. Do not fall back to unverified TLS or automatically connect to arbitrary URLs from telemetry. |

For a new model, start with read-only telemetry and add a profile based on
observed identity and status. Add storage and camera support only when their
transports are verified. Advertise physical controls and print dispatch only
after validating model limits, the command wire format, and rejection and
confirmation behavior. A successful MQTT connection alone is not evidence of
support for those operations.

[presets]: https://github.com/bambulab/BambuStudio/tree/8e524a39d4c4cd0541801c1c46d2f77debb0bf1d/resources/profiles/BBL/machine
[identity]: https://github.com/bambulab/BambuStudio/blob/8e524a39d4c4cd0541801c1c46d2f77debb0bf1d/src/slic3r/GUI/DeviceManager.cpp#L2945-L2998
[controls]: https://github.com/bambulab/BambuStudio/blob/8e524a39d4c4cd0541801c1c46d2f77debb0bf1d/src/slic3r/GUI/DeviceManager.cpp#L1465-L1678
[nozzles]: https://github.com/bambulab/BambuStudio/blob/8e524a39d4c4cd0541801c1c46d2f77debb0bf1d/src/slic3r/GUI/DeviceCore/DevNozzleSystem.cpp#L1109-L1252
[mapping]: https://github.com/bambulab/BambuStudio/blob/8e524a39d4c4cd0541801c1c46d2f77debb0bf1d/src/slic3r/GUI/SelectMachine.cpp#L3695-L3723
[storage]: https://github.com/bambulab/BambuStudio/blob/8e524a39d4c4cd0541801c1c46d2f77debb0bf1d/src/slic3r/GUI/MediaFilePanel.cpp#L267-L282
[printjob]: https://github.com/bambulab/BambuStudio/blob/8e524a39d4c4cd0541801c1c46d2f77debb0bf1d/src/slic3r/GUI/Jobs/PrintJob.cpp#L770-L890
[plugin]: https://github.com/bambulab/BambuStudio/blob/8e524a39d4c4cd0541801c1c46d2f77debb0bf1d/src/slic3r/Utils/NetworkAgent.cpp#L350-L370
[camera-telemetry]: https://github.com/bambulab/BambuStudio/blob/8e524a39d4c4cd0541801c1c46d2f77debb0bf1d/src/slic3r/GUI/DeviceManager.cpp#L3650-L3690
[camera]: https://github.com/bambulab/BambuStudio/blob/8e524a39d4c4cd0541801c1c46d2f77debb0bf1d/src/slic3r/GUI/MediaPlayCtrl.cpp#L585-L595
