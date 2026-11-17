// Package bambucamera adds explicit TLS trust to the released host RTSP adapter.
// Authorization and WebRTC remain host responsibilities.
package bambucamera

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/prusa3d/prusa-connect/adapters/camera/rtsp"
	"github.com/prusa3d/prusa-connect/sdk/cameraapi"
	"github.com/prusa3d/prusa-connect/sdk/extension"
	"github.com/skaronator/panda-connect/internal/printertls"
	"github.com/skaronator/panda-connect/internal/secret"
)

const Type = "panda-connect-rtsps"

var errCamera = errors.New("Bambu local camera unavailable; check LAN liveview, access-code file and camera certificate trust")

func init() {
	doc := extension.Doc{
		Summary:     "Bambu local H.264 RTSPS liveview with explicit TLS certificate trust.",
		Description: "Uses the released RTSP adapter and host authorization. Offers H.264 WebRTC and, when FFmpeg is installed, HTTP MJPEG for browsers without a WebRTC media path. No snapshots or H.265 conversion.",
		Version:     "0.2.0", Contract: cameraapi.Contract, Stability: extension.StabilityExperimental,
		Options: []extension.Option{
			{Key: "address", Type: extension.OptionString, Required: true, Summary: "The printer's private LAN IP, without a scheme, port or credentials."},
			{Key: "access_code_file", Type: extension.OptionString, Required: true, Summary: "File containing the printer access code."},
			{Key: "tls_fingerprint", Type: extension.OptionString, Summary: "Enrolled SHA-256 camera certificate pin; required unless ca_file is set."},
			{Key: "ca_file", Type: extension.OptionString, Summary: "Alternative PEM trust bundle; normal hostname/IP verification applies."},
			{Key: "tls_server_name", Type: extension.OptionString, Summary: "Expected certificate name with CA verification."},
		},
	}
	cameraapi.Register(Type, doc, New)
	// Existing host camera records keep their type.
	doc.Summary = "Compatibility alias for existing Bambu local cameras."
	cameraapi.Register("pandalink-rtsps", doc, New)
	cameraapi.Register("pandalink-h3c", doc, New)
}

type Camera struct {
	address, secretFile string
	ffmpeg              string
	tls                 *tls.Config
	life                context.Context
	cancel              context.CancelFunc
}

var _ cameraapi.Camera = (*Camera)(nil)

func New(spec cameraapi.ProviderSpec) (cameraapi.Camera, error) {
	if spec.Printer == "" {
		return nil, errors.New("Bambu camera must be bound to a printer for host authorization")
	}
	options := map[string]string{}
	for k, v := range spec.Config {
		if k == "printer" {
			if v != spec.Printer {
				return nil, errors.New("camera printer binding differs from the host binding")
			}
			continue
		}
		switch k {
		case "address", "access_code_file", "tls_fingerprint", "ca_file", "tls_server_name":
		default:
			return nil, errors.New("unsupported Bambu camera option; URLs and inline credentials are not accepted")
		}
		text, ok := v.(string)
		if !ok {
			return nil, errors.New("Bambu camera options must be strings")
		}
		options[k] = text
	}
	ip := net.ParseIP(options["address"])
	if ip == nil || (!ip.IsPrivate() && !ip.IsLoopback()) || ip.IsUnspecified() {
		return nil, errors.New("Bambu camera address must be a private LAN IP")
	}
	if options["access_code_file"] == "" {
		return nil, errors.New("Bambu camera requires access_code_file")
	}
	cfg, err := printertls.Config(options)
	if err != nil {
		return nil, err
	}
	if cfg.ServerName == "" {
		cfg.ServerName = ip.String()
	}
	life, cancel := context.WithCancel(context.Background())
	ffmpeg, _ := exec.LookPath("ffmpeg")
	return &Camera{address: net.JoinHostPort(ip.String(), "322"), secretFile: options["access_code_file"], ffmpeg: ffmpeg, tls: cfg, life: life, cancel: cancel}, nil
}
func (c *Camera) Formats() []cameraapi.Format {
	formats := []cameraapi.Format{cameraapi.FormatH264AnnexB}
	if c.ffmpeg != "" {
		formats = append(formats, cameraapi.FormatMJPEG)
	}
	return formats
}
func (c *Camera) Snapshot(context.Context) (cameraapi.Snapshot, error) {
	return cameraapi.Snapshot{}, cameraapi.ErrFormatNotSupported
}
func (c *Camera) Close() error { c.cancel(); return nil }

// The released RTSP adapter cannot accept a pin/custom tls.Config. One ephemeral
// single-connection loopback relay per Open adds authenticated TLS. It is not
// exposed on the LAN, is never shared, and dies with the stream.
func (c *Camera) Open(ctx context.Context, format cameraapi.Format) (cameraapi.Stream, error) {
	if format == cameraapi.FormatMJPEG && c.ffmpeg != "" {
		return c.openMJPEG(ctx)
	}
	if format != cameraapi.FormatH264AnnexB {
		return cameraapi.Stream{}, cameraapi.ErrFormatNotSupported
	}
	return c.openH264(ctx)
}

func (c *Camera) openH264(ctx context.Context) (cameraapi.Stream, error) {
	const format = cameraapi.FormatH264AnnexB
	// The host transfers Body to WebRTC and returns the SDP response. The
	// HTTP request context ends at that point, not when the viewer leaves.
	// Observe request cancellation only until Open completes.
	request := ctx
	ctx, cancel := context.WithCancel(context.WithoutCancel(request))
	stopOpen := context.AfterFunc(request, cancel)
	defer stopOpen()
	stopLife := context.AfterFunc(c.life, cancel)
	if request.Err() != nil || c.life.Err() != nil {
		cancel()
	}
	cleanupFailure := true
	defer func() {
		if cleanupFailure {
			stopLife()
			cancel()
		}
	}()
	code, err := secret.Read(c.secretFile)
	if err != nil {
		return cameraapi.Stream{}, errCamera
	}
	handshake, stopHandshake := context.WithTimeout(ctx, 5*time.Second)
	remote, err := (&tls.Dialer{Config: c.tls.Clone(), NetDialer: &net.Dialer{Timeout: 5 * time.Second}}).DialContext(handshake, "tcp", c.address)
	stopHandshake()
	if err != nil {
		return cameraapi.Stream{}, errCamera
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		_ = remote.Close()
		return cameraapi.Stream{}, errCamera
	}
	var mu sync.Mutex
	var local net.Conn
	closeSockets := func() {
		_ = listener.Close()
		_ = remote.Close()
		mu.Lock()
		if local != nil {
			_ = local.Close()
		}
		mu.Unlock()
	}
	stopSockets := context.AfterFunc(ctx, closeSockets)
	cleanup := func() { cancel(); stopLife(); stopSockets(); closeSockets() }
	go func() {
		conn, err := listener.Accept()
		_ = listener.Close()
		if err != nil {
			return
		}
		mu.Lock()
		local = conn
		if ctx.Err() != nil {
			_ = conn.Close()
		}
		mu.Unlock()
		go func() {
			_ = relayRequests(remote, conn, "rtsp://"+listener.Addr().String(), "rtsps://"+c.address)
			cleanup()
		}()
		_, _ = io.Copy(conn, remote)
		cleanup()
	}()
	upstream, err := rtsp.New(cameraapi.ProviderSpec{Config: map[string]any{
		"url":      "rtsp://" + listener.Addr().String() + "/streaming/live/1",
		"username": "bblp", "password": code, "timeout": "5s",
	}})
	if err != nil {
		cleanup()
		return cameraapi.Stream{}, errCamera
	}
	stream, err := upstream.Open(ctx, format)
	if err != nil || !stopOpen() || ctx.Err() != nil {
		cleanup()
		_ = upstream.Close()
		if stream.Body != nil {
			_ = stream.Body.Close()
		}
		return cameraapi.Stream{}, errCamera
	}
	cleanupFailure = false
	rawBody := stream.Body
	stopStream := context.AfterFunc(ctx, func() { _ = upstream.Close(); _ = rawBody.Close() })
	stream.Body = &body{ReadCloser: rawBody, cleanup: func() { stopStream(); cleanup(); _ = upstream.Close() }}
	return stream, nil
}

// The H3C redirects requests whose request-target names the loopback relay.
// Rewrite only the request line, not SDP, Digest headers or interleaved RTP.
// The released client sends header-only RTSP requests on this leg.
func relayRequests(dst io.Writer, src io.Reader, local, printer string) error {
	r := bufio.NewReaderSize(src, 8192)
	start := true
	for {
		line, err := r.ReadSlice('\n')
		if err != nil {
			return err
		}
		out := string(line)
		if start {
			method, rest, ok := strings.Cut(out, " ")
			uri, version, valid := strings.Cut(rest, " ")
			if !ok || !valid || version != "RTSP/1.0\r\n" {
				return errCamera
			}
			if path, ok := strings.CutPrefix(uri, local+"/"); ok {
				out = method + " " + printer + "/" + path + " " + version
			}
		}
		start = out == "\r\n"
		if _, err := io.WriteString(dst, out); err != nil {
			return err
		}
	}
}

type body struct {
	io.ReadCloser
	once    sync.Once
	cleanup func()
}

func (b *body) Close() error {
	var err error
	b.once.Do(func() { b.cleanup(); err = b.ReadCloser.Close() })
	return err
}
