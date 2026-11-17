package h3c

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"path"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prusa3d/prusa-connect/sdk/connectorapi"
	"github.com/prusa3d/prusa-connect/sdk/fault"
)

type fakeFTPS struct {
	mu            sync.Mutex
	files         map[string][]byte
	connections   []net.Conn
	listeners     []net.Listener
	wg            sync.WaitGroup
	badSize, drop atomic.Bool
	requests      atomic.Uint64
	resumed       atomic.Uint64
}

func startFTPS(t *testing.T, p *fakePrinter) *fakeFTPS {
	t.Helper()
	cfg := &tls.Config{Certificates: []tls.Certificate{p.certificate}, MinVersion: tls.VersionTLS12, MaxVersion: tls.VersionTLS12}
	l, err := tls.Listen("tcp", "127.0.0.1:0", cfg)
	if err != nil {
		t.Fatal(err)
	}
	_, port, _ := net.SplitHostPort(l.Addr().String())
	p.endpoint.Options["ftps_port"] = port
	p.endpoint.Options["allow_uploads"] = "true"
	f := &fakeFTPS{files: map[string][]byte{}, listeners: []net.Listener{l}}
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
			go func() { defer f.wg.Done(); defer c.Close(); f.serve(t, c, cfg) }()
		}
	}()
	t.Cleanup(func() {
		f.mu.Lock()
		for _, l := range f.listeners {
			_ = l.Close()
		}
		for _, c := range f.connections {
			_ = c.Close()
		}
		f.mu.Unlock()
		f.wg.Wait()
	})
	return f
}

func TestStorageReportAndUploadDefault(t *testing.T) {
	p := startFake(t)
	f := startFTPS(t, p)
	delete(p.endpoint.Options, "allow_uploads")
	s := connectFake(t, p, time.Second)
	if held, err := s.Storages(t.Context()); err != nil || len(held) != 0 || s.StoragesReported() {
		t.Fatalf("unverified storage reported: %v %v", held, err)
	}
	if err := s.Send(t.Context(), connectorapi.Upload{Path: "sdcard/test.3mf"}); !errors.Is(err, connectorapi.ErrNoTransfers) || f.requests.Load() != 0 {
		t.Fatalf("upload was not refused locally: %v", err)
	}
	if err := s.AskStorages(t.Context()); err != nil {
		t.Fatal(err)
	}
	if held, err := s.Storages(t.Context()); err != nil || len(held) != 1 || !s.StoragesReported() {
		t.Fatalf("storage report missing: %v %v", held, err)
	}
}
func (f *fakeFTPS) serve(t *testing.T, c net.Conn, cfg *tls.Config) {
	write := func(s string) { _, _ = io.WriteString(c, s+"\r\n") }
	write("220 fake H3C")
	r := bufio.NewReader(c)
	var passive net.Listener
	protected := false
	defer func() {
		if passive != nil {
			_ = passive.Close()
		}
	}()
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		command, arg, _ := strings.Cut(line, " ")
		f.requests.Add(1)
		switch command {
		case "USER":
			write("331 password required")
		case "PASS":
			if arg != "test-code" {
				write("530 refused")
				return
			}
			write("230 logged in")
		case "PBSZ", "TYPE":
			write("200 OK")
		case "PROT":
			protected = arg == "P"
			write("200 OK")
		case "EPSV":
			passive, err = tls.Listen("tcp", "127.0.0.1:0", cfg)
			if err != nil {
				return
			}
			f.mu.Lock()
			f.listeners = append(f.listeners, passive)
			f.mu.Unlock()
			_, port, _ := net.SplitHostPort(passive.Addr().String())
			write("229 Entering Extended Passive Mode (|||" + port + "|)")
		case "LIST", "STOR", "RETR":
			if passive == nil || !protected {
				t.Error("unprotected or non-passive transfer")
				return
			}
			write("150 data follows")
			data, err := passive.Accept()
			_ = passive.Close()
			passive = nil
			if err != nil {
				return
			}
			f.mu.Lock()
			f.connections = append(f.connections, data)
			f.mu.Unlock()
			secure := data.(*tls.Conn)
			if err = secure.Handshake(); err != nil {
				_ = data.Close()
				return
			}
			if !secure.ConnectionState().DidResume {
				t.Error("FTPS data channel did not reuse control TLS session")
				_ = data.Close()
				return
			}
			f.resumed.Add(1)
			if f.drop.Load() {
				_ = data.Close()
				return
			}
			switch command {
			case "LIST":
				f.mu.Lock()
				names := map[string]bool{}
				for name, content := range f.files {
					if path.Dir(name) == arg {
						size := len(content)
						if f.badSize.Load() {
							size++
						}
						_, _ = fmt.Fprintf(data, "-rw-r--r-- 1 user group %d Nov 17 12:00 %s\r\n", size, path.Base(name))
					}
					if arg == "/" && strings.HasPrefix(name, "/cache/") {
						names["cache"] = true
					}
				}
				if arg == "/" {
					names["cache"] = true
				}
				for name := range names {
					_, _ = fmt.Fprintf(data, "drwxr-xr-x 1 user group 0 Nov 17 12:00 %s\r\n", name)
				}
				f.mu.Unlock()
			case "STOR":
				b, err := io.ReadAll(data)
				if err != nil {
					_ = data.Close()
					return
				}
				f.mu.Lock()
				f.files[arg] = b
				f.mu.Unlock()
			case "RETR":
				f.mu.Lock()
				b := append([]byte(nil), f.files[arg]...)
				f.mu.Unlock()
				_, _ = data.Write(b)
			}
			_ = data.Close()
			write("226 transfer finished")
		default:
			t.Errorf("unexpected FTP verb %s", command)
			return
		}
	}
}

type testContent struct {
	bytes  []byte
	opened atomic.Bool
}

func (c *testContent) Size() int64 { return int64(len(c.bytes)) }
func (c *testContent) Open(ctx context.Context, offset int64) (io.ReadCloser, error) {
	c.opened.Store(true)
	return io.NopCloser(bytes.NewReader(c.bytes[min(offset, int64(len(c.bytes))):])), ctx.Err()
}

func TestFTPSUploadAndVerify(t *testing.T) {
	p := startFake(t)
	f := startFTPS(t, p)
	s := connectFake(t, p, time.Second)
	content := &testContent{bytes: bytes.Repeat([]byte("streamed data"), 10000)}
	var progress []connectorapi.Progress
	err := s.Send(t.Context(), connectorapi.Upload{Path: "sdcard/cache/single plate.gcode.3mf", Content: content, Progress: func(p connectorapi.Progress) { progress = append(progress, p) }})
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	b := f.files["/cache/single plate.gcode.3mf"]
	f.mu.Unlock()
	if !bytes.Equal(b, content.bytes) {
		t.Fatal("wrong uploaded bytes")
	}
	last := progress[len(progress)-1]
	if !last.Known || last.Received != content.Size() || last.Sent != content.Size() {
		t.Fatalf("progress: %+v", last)
	}
	for i, p := range progress {
		if p.Sent > p.Total || (i > 0 && p.Sent < progress[i-1].Sent) {
			t.Fatal("non-monotonic progress")
		}
	}
	if f.resumed.Load() < 3 {
		t.Fatal("missing encrypted listing/upload/verification")
	}
	listing, err := s.List(t.Context(), "sdcard/cache")
	if err != nil || len(listing.Entries) != 1 || listing.Entries[0].Size != content.Size() {
		t.Fatalf("listing: %+v %v", listing, err)
	}
	if err = s.Send(t.Context(), connectorapi.Upload{Path: "sdcard/cache/single plate.gcode.3mf", Content: content}); !errors.Is(err, connectorapi.ErrTransferRefused) {
		t.Fatalf("overwrote an existing file: %v", err)
	}
}
func TestFTPSFailures(t *testing.T) {
	for _, mode := range []string{"wrong size", "disconnect", "pin"} {
		t.Run(mode, func(t *testing.T) {
			p := startFake(t)
			f := startFTPS(t, p)
			s := connectFake(t, p, time.Second)
			if mode == "wrong size" {
				f.badSize.Store(true)
			}
			if mode == "disconnect" {
				f.drop.Store(true)
			}
			if mode == "pin" {
				s.storage.tls, _ = printerTLS(map[string]string{"tls_fingerprint": strings.Repeat("0", 64)})
			}
			err := s.Send(t.Context(), connectorapi.Upload{Path: "sdcard/cache/test.3mf", Content: &testContent{bytes: []byte("data")}})
			if err == nil {
				t.Fatal("unverified upload succeeded")
			}
			if mode == "wrong size" && !fault.HasCode(err, connectorapi.TransferNotOnStorage.Code()) {
				t.Fatalf("wrong fault: %v", err)
			}
		})
	}
}

type blockedContent struct {
	opened chan struct{}
	closed chan struct{}
	once   sync.Once
}

func (c *blockedContent) Size() int64 { return 100 }
func (c *blockedContent) Open(context.Context, int64) (io.ReadCloser, error) {
	close(c.opened)
	return c, nil
}
func (c *blockedContent) Read([]byte) (int, error) { <-c.closed; return 0, context.Canceled }
func (c *blockedContent) Close() error             { c.once.Do(func() { close(c.closed) }); return nil }
func TestFTPSBusyCancellationAndClose(t *testing.T) {
	for _, end := range []string{"cancel", "close", "timeout"} {
		t.Run(end, func(t *testing.T) {
			p := startFake(t)
			startFTPS(t, p)
			s := connectFake(t, p, time.Second)
			c := &blockedContent{opened: make(chan struct{}), closed: make(chan struct{})}
			ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- s.Send(ctx, connectorapi.Upload{Path: "sdcard/cache/blocked.3mf", Content: c}) }()
			select {
			case <-c.opened:
			case <-time.After(time.Second):
				t.Fatal("content not opened")
			}
			if err := s.Send(t.Context(), connectorapi.Upload{Path: "sdcard/cache/other.3mf", Content: &testContent{bytes: []byte("x")}}); !errors.Is(err, connectorapi.ErrTransferBusy) {
				t.Fatal(err)
			}
			switch end {
			case "cancel":
				cancel()
			case "close":
				_ = s.Close()
			}
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("unfinished upload succeeded")
				}
			case <-time.After(2 * time.Second):
				t.Fatal("transfer leaked")
			}
		})
	}
}
func TestPathsNeverReachFTPS(t *testing.T) {
	p := startFake(t)
	f := startFTPS(t, p)
	s := connectFake(t, p, time.Second)
	for i, p := range []string{"/sdcard/a", "sdcard/../a", "sdcard/a/../../x", "sdcard/a\r\nDELE /x", "sdcard/a\x00", "sdcard/a\\b", "sdcard/%2e%2e/a", "sdcard//a", "sdcard/./a", "sdcard/ cache", "elsewhere/a"} {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			content := &testContent{bytes: []byte("x")}
			if err := s.Send(t.Context(), connectorapi.Upload{Path: p, Content: content}); !fault.HasCode(err, connectorapi.PathNotAllowed.Code()) {
				t.Fatalf("unsafe upload: %v", err)
			}
			if _, err := s.List(t.Context(), p); !fault.HasCode(err, connectorapi.PathNotAllowed.Code()) {
				t.Fatalf("unsafe listing: %v", err)
			}
			if content.opened.Load() {
				t.Fatal("opened content for unsafe path")
			}
		})
	}
	if f.requests.Load() != 0 {
		t.Fatal("unsafe path reached printer")
	}
}
