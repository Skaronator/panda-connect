package bambucamera

import (
	"bufio"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http/httptest"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prusa3d/prusa-connect/sdk/cameraapi"
	"github.com/prusa3d/prusa-connect/sdk/cameraapi/cameraapitest"
)

type cameraFake struct {
	address, pin string
	mu           sync.Mutex
	connections  []net.Conn
	wg           sync.WaitGroup
	refuse       atomic.Bool
}

func startCamera(t *testing.T) *cameraFake {
	t.Helper()
	certServer := httptest.NewTLSServer(nil)
	cert := certServer.TLS.Certificates[0]
	certServer.Close()
	l, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(cert.Certificate[0])
	f := &cameraFake{address: l.Addr().String(), pin: hex.EncodeToString(sum[:])}
	f.wg.Add(1)
	go func() {
		defer f.wg.Done()
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			f.mu.Lock()
			f.connections = append(f.connections, c)
			f.mu.Unlock()
			f.wg.Add(1)
			go func() { defer f.wg.Done(); defer c.Close(); f.serve(c) }()
		}
	}()
	t.Cleanup(func() {
		_ = l.Close()
		f.mu.Lock()
		for _, c := range f.connections {
			_ = c.Close()
		}
		f.mu.Unlock()
		f.wg.Wait()
	})
	return f
}

func (f *cameraFake) serve(c net.Conn) {
	r := textproto.NewReader(bufio.NewReader(c))
	for {
		line, err := r.ReadLine()
		if err != nil {
			return
		}
		headers, err := r.ReadMIMEHeader()
		if err != nil {
			return
		}
		method, target, _ := strings.Cut(line, " ")
		if !strings.HasPrefix(target, "rtsps://"+f.address+"/streaming/live/1") {
			_, _ = fmt.Fprint(c, "RTSP/1.0 301 Moved Permanently\r\nContent-Length: 0\r\n\r\n")
			return
		}
		seq := headers.Get("Cseq")
		if headers.Get("Authorization") != "Basic "+base64.StdEncoding.EncodeToString([]byte("bblp:test-code")) || f.refuse.Load() {
			_, _ = fmt.Fprintf(c, "RTSP/1.0 401 Unauthorized\r\nCSeq: %s\r\nWWW-Authenticate: Basic realm=\"fake\"\r\nContent-Length: 0\r\n\r\n", seq)
			continue
		}
		var body, extra string
		switch method {
		case "DESCRIBE":
			body = "v=0\r\nm=video 0 RTP/AVP 96\r\na=rtpmap:96 H264/90000\r\na=control:trackID=0\r\na=fmtp:96 packetization-mode=1;sprop-parameter-sets=Z0IAH5WoFAFuQA==,aM4G4g==\r\n"
			extra = "Content-Type: application/sdp\r\n"
		case "SETUP":
			extra = "Session: test;timeout=30\r\nTransport: RTP/AVP/TCP;unicast;interleaved=0-1\r\n"
		case "PLAY":
			extra = "Session: test\r\n"
		}
		_, err = fmt.Fprintf(c, "RTSP/1.0 200 OK\r\nCSeq: %s\r\n%sContent-Length: %d\r\n\r\n%s", seq, extra, len(body), body)
		if err != nil || method == "TEARDOWN" {
			return
		}
	}
}

func newTestCamera(t *testing.T, f *cameraFake) *Camera {
	t.Helper()
	file := filepath.Join(t.TempDir(), "code")
	if err := os.WriteFile(file, []byte("test-code"), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := New(cameraapi.ProviderSpec{Printer: "test-printer", Config: map[string]any{"address": "127.0.0.1", "access_code_file": file, "tls_fingerprint": f.pin}})
	if err != nil {
		t.Fatal(err)
	}
	c.(*Camera).address = f.address
	t.Cleanup(func() { _ = c.Close() })
	return c.(*Camera)
}

func TestCameraContract(t *testing.T) {
	f := startCamera(t)
	cameraapitest.RunCameraContract(t, func(t *testing.T) cameraapi.Camera {
		c := newTestCamera(t, f)
		// The RTSP fake provides parameter sets only, not decodable frames.
		// Real JPEG output is exercised in TestHTTPMJPEGFrames.
		c.ffmpeg = ""
		return c
	})
}

func TestCameraTrustAndRedaction(t *testing.T) {
	for _, mode := range []string{"pin", "missing secret", "unauthorized", "connection refused"} {
		t.Run(mode, func(t *testing.T) {
			f := startCamera(t)
			c := newTestCamera(t, f)
			switch mode {
			case "pin":
				c.tls.VerifyConnection = func(tls.ConnectionState) error { return errors.New("test-code must never be exposed") }
			case "missing secret":
				c.secretFile += "missing"
			case "unauthorized":
				f.refuse.Store(true)
			case "connection refused":
				c.address = "127.0.0.1:1"
			}
			_, err := c.Open(t.Context(), cameraapi.FormatH264AnnexB)
			if err == nil || strings.Contains(err.Error(), "test-code") || strings.Contains(err.Error(), "rtsp://") {
				t.Fatalf("unsafe failure: %v", err)
			}
		})
	}
}

func TestCameraRejectsURLsAndOtherHosts(t *testing.T) {
	for _, address := range []string{"https://192.168.1.1", "rtsps://bblp:secret@192.168.1.1/streaming/live/1", "192.168.1.1:322", "printer.local", "8.8.8.8", "0.0.0.0", "192.168.1.1\r\n"} {
		_, err := New(cameraapi.ProviderSpec{Printer: "p", Config: map[string]any{"address": address, "access_code_file": "file", "tls_fingerprint": strings.Repeat("0", 64)}})
		if err == nil || strings.Contains(err.Error(), "secret@") {
			t.Fatalf("accepted or exposed invalid camera target: %v", err)
		}
	}
	if _, err := New(cameraapi.ProviderSpec{Config: map[string]any{"address": "192.168.1.1"}}); err == nil {
		t.Fatal("accepted unbound camera")
	}
	if _, err := New(cameraapi.ProviderSpec{Printer: "p", Config: map[string]any{"url": "rtsps://secret@elsewhere"}}); err == nil || strings.Contains(err.Error(), "secret@") {
		t.Fatal("accepted or exposed arbitrary URL")
	}
}

func TestCameraRequestHandoffAndReopen(t *testing.T) {
	f := startCamera(t)
	c := newTestCamera(t, f)
	cancelled, stop := context.WithCancel(t.Context())
	stop()
	if stream, err := c.Open(cancelled, cameraapi.FormatH264AnnexB); err == nil {
		_ = stream.Body.Close()
		t.Fatal("opened with a cancelled request")
	}
	for i := 0; i < 2; i++ {
		ctx, cancel := context.WithCancel(t.Context())
		stream, err := c.Open(ctx, cameraapi.FormatH264AnnexB)
		if err != nil {
			t.Fatal(err)
		}
		cancel() // SDP response has been written; the viewer still owns Body.
		b := make([]byte, 4)
		if _, err = io.ReadFull(stream.Body, b); err != nil || string(b) != "\x00\x00\x00\x01" {
			t.Fatal("missing Annex-B units", err)
		}
		done := make(chan struct{})
		go func() { _, _ = io.Copy(io.Discard, stream.Body); close(done) }()
		if i == 0 {
			_ = stream.Body.Close()
		} else {
			_ = c.Close()
		}
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("closed reader stayed blocked")
		}
		_ = stream.Body.Close()
	}
	_ = c.Close()
	if _, err := c.Open(t.Context(), cameraapi.FormatH264AnnexB); err == nil {
		t.Fatal("closed camera reopened")
	}
}
