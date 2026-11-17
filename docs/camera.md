# H3C live camera

For new configurations, use the host camera type `pandalink-rtsps`. Existing
`pandalink-h3c` records remain supported as an alias and need no migration.
This type is for the verified RTSPS workflow, not every Bambu camera.
It serves H.264 over WebRTC
and, with FFmpeg installed, MJPEG over HTTP. The Compose image includes FFmpeg.
There are no snapshots, audio or H.265 conversion. LAN liveview must
already be available on the printer. Camera settings do not change printer
settings.

Read the connected printer ID from `GET /api/v1/devices`. Add a camera with
`PUT /api/v1/cameras/h3c-live`, `Content-Type: application/json` and
`If-None-Match: *`. Use the authorized host session or bearer token where login
is enabled. Example body:

```json
{
  "type": "pandalink-rtsps",
  "config": {
    "printer": "<host printer ID>",
    "address": "192.168.1.100",
    "access_code_file": "/run/secrets/bambu_access_code",
    "tls_fingerprint": "<verified SHA-256 camera certificate fingerprint>"
  }
}
```

Use the printer's private IP with no scheme or port. The adapter uses RTSPS
port 322 and `/streaming/live/1`. Verify the camera certificate before pinning
it, even if it matches the MQTT pin. Instead of the fingerprint you can use a
trusted `ca_file` and optionally `tls_server_name`. Never include the access
code in camera config or URLs. A single-connection loopback relay supplies
TLS verification for the released RTSP adapter. Keep the container network
namespace isolated from untrusted processes; the internal relay is plaintext.

`GET /api/v1/cameras?device=<id>` should show `watch: true` after activation.
When it also shows `stream: true`, the dashboard and printer camera tiles use
the HTTP stream. Select `h3c-live` and press **Play**; the tile is blank before
playback because the adapter does not supply thumbnails. The HTTP stream needs
no UDP forwarding and works over the same connection as the dashboard. It
converts live video to up to 960 pixels wide at 5 fps. Use **Stop** or leave
the page when finished.

To use WebRTC instead, post a receive-only H.264 SDP offer as
`application/sdp` to `/api/v1/cameras/h3c-live/webrtc`. The response is the SDP
answer. The client must be able to reach the host's ICE candidates, including
its UDP ports; the Docker HTTP port mapping alone does not guarantee that a
browser outside the Docker host can receive video. Close the peer connection
when leaving the view. Delete the camera with `DELETE /api/v1/cameras/h3c-live`
and `If-Match` quoting its current version from `GET /api/v1/camera-settings`.

The host owns camera authorization and the printer binding. A camera can also
have explicit grants or blocks in the host. Enable host authentication before
exposing camera or printer routes; anyone able to reach the default Compose
API has its local operator permissions.
