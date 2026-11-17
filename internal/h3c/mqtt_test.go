package h3c

import (
	"bufio"
	"crypto/sha256"
	"crypto/tls"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/prusa3d/prusa-connect/sdk/connectorapi"
)

// The fake speaks MQTT over real TLS. Controls require an explicit test handler;
// ordinary monitoring/contract tests fail if a physical command is sent.
type fakePrinter struct {
	listener        net.Listener
	mu              sync.Mutex
	connections     map[*fakeConnection]bool
	wg              sync.WaitGroup
	endpoint        connectorapi.Endpoint
	quiet           atomic.Bool
	requests        atomic.Uint64
	version, report []byte
	control         func(*fakeConnection, map[string]json.RawMessage)
	certificate     tls.Certificate
}

type fakeConnection struct {
	net.Conn
	writeMu sync.Mutex
}

func startFake(t *testing.T) *fakePrinter {
	t.Helper()
	certificateServer := httptest.NewTLSServer(nil)
	cert := certificateServer.TLS.Certificates[0]
	certificateServer.Close()
	listener, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
	if err != nil {
		t.Fatal(err)
	}
	f := &fakePrinter{listener: listener, connections: make(map[*fakeConnection]bool), version: fixture(t, "h3c-version.json"), report: fixture(t, "h3c-running.json")}
	f.certificate = cert
	codeFile := filepath.Join(t.TempDir(), "access_code")
	if err = os.WriteFile(codeFile, []byte("test-code\n"), 0600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(cert.Certificate[0])
	f.endpoint = connectorapi.Endpoint{Address: listener.Addr().String(), Options: map[string]string{
		"serial": "H3C-TEST-001", "access_code_file": codeFile, "tls_fingerprint": hex.EncodeToString(sum[:]),
	}}
	f.wg.Add(1)
	go func() {
		defer f.wg.Done()
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			c := &fakeConnection{Conn: conn}
			f.mu.Lock()
			f.connections[c] = true
			f.mu.Unlock()
			f.wg.Add(1)
			go func() { defer f.wg.Done(); defer conn.Close(); f.serve(t, c) }()
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		f.mu.Lock()
		for c := range f.connections {
			_ = c.Close()
		}
		f.mu.Unlock()
		f.wg.Wait()
	})
	return f
}

func (f *fakePrinter) serve(t *testing.T, c *fakeConnection) {
	r := bufio.NewReader(c)
	for {
		header, payload, err := readPacket(r)
		if err != nil {
			return
		}
		switch header >> 4 {
		case 1:
			if len(payload) < 10 {
				return
			}
			_, remaining, err := mqttString(payload[10:])
			if err != nil {
				return
			}
			user, remaining, err := mqttString(remaining)
			if err != nil {
				return
			}
			pass, _, err := mqttString(remaining)
			if err != nil || user != "bblp" || pass != "test-code" {
				_ = c.send(0x20, []byte{0, 4})
				return
			}
			if c.send(0x20, []byte{0, 0}) != nil {
				return
			}
		case 8:
			if len(payload) < 2 {
				return
			}
			if c.send(0x90, append(append([]byte{}, payload[:2]...), 0)) != nil {
				return
			}
		case 3:
			topic, body, err := mqttString(payload)
			if err != nil {
				return
			}
			if topic != "device/H3C-TEST-001/request" {
				t.Error("unexpected request topic")
				return
			}
			var command map[string]struct {
				Command string `json:"command"`
			}
			if err = json.Unmarshal(body, &command); err != nil {
				t.Error(err)
				return
			}
			f.requests.Add(1)
			if info, ok := command["info"]; ok && info.Command == "get_version" {
				_ = f.sendTo(c, f.version)
			} else if push, ok := command["pushing"]; ok && push.Command == "pushall" {
				if !f.quiet.Load() {
					_ = f.sendTo(c, f.report)
				}
			} else {
				var envelope map[string]map[string]json.RawMessage
				if json.Unmarshal(body, &envelope) == nil && envelope["print"] != nil && f.control != nil {
					f.control(c, envelope["print"])
					continue
				}
				t.Error("driver sent a command other than get_version or pushall")
				return
			}
		case 12:
			_ = c.send(0xd0, nil)
		case 14:
			return
		default:
			t.Errorf("unexpected MQTT packet type %d", header>>4)
			return
		}
	}
}

func (f *fakePrinter) sendTo(c *fakeConnection, frame []byte) error {
	topic := "device/H3C-TEST-001/report"
	return c.send(0x30, append(append([]byte{byte(len(topic) >> 8), byte(len(topic))}, topic...), frame...))
}

func (f *fakePrinter) disconnect() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for c := range f.connections {
		_ = c.Close()
	}
}

func (c *fakeConnection) send(header byte, payload []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	frame := []byte{header}
	for n := len(payload); ; {
		digit := byte(n % 128)
		n /= 128
		if n != 0 {
			digit |= 128
		}
		frame = append(frame, digit)
		if n == 0 {
			break
		}
	}
	_, err := c.Write(append(frame, payload...))
	return err
}

func readPacket(r *bufio.Reader) (byte, []byte, error) {
	header, err := r.ReadByte()
	if err != nil {
		return 0, nil, err
	}
	n, factor := 0, 1
	for i := 0; i < 4; i++ {
		digit, err := r.ReadByte()
		if err != nil {
			return 0, nil, err
		}
		n += int(digit&127) * factor
		factor *= 128
		if n > 1<<20 {
			return 0, nil, fmt.Errorf("packet too large")
		}
		if digit&128 == 0 {
			body := make([]byte, n)
			_, err = io.ReadFull(r, body)
			return header, body, err
		}
	}
	return 0, nil, fmt.Errorf("invalid length")
}

func mqttString(payload []byte) (string, []byte, error) {
	if len(payload) < 2 {
		return "", nil, io.ErrUnexpectedEOF
	}
	n := int(binary.BigEndian.Uint16(payload[:2]))
	if len(payload) < n+2 {
		return "", nil, io.ErrUnexpectedEOF
	}
	return string(payload[2 : n+2]), payload[n+2:], nil
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}
