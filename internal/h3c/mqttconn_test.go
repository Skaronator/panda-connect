package h3c

import (
	"bytes"
	"io"
	"net"
	"testing"
	"time"
)

// A byte reader behind net.Conn lets the header guard be tested independently
// of the fake broker and Paho's packet decoder.
type byteConn struct{ *bytes.Reader }

func (byteConn) Write(p []byte) (int, error)      { return len(p), nil }
func (byteConn) Close() error                     { return nil }
func (byteConn) LocalAddr() net.Addr              { return nil }
func (byteConn) RemoteAddr() net.Addr             { return nil }
func (byteConn) SetDeadline(time.Time) error      { return nil }
func (byteConn) SetReadDeadline(time.Time) error  { return nil }
func (byteConn) SetWriteDeadline(time.Time) error { return nil }

func TestMQTTFrameLimit(t *testing.T) {
	t.Run("multiple frames", func(t *testing.T) {
		wire := []byte{0x30, 3, 1, 2, 3, 0xd0, 0, 0x30, 1, 4}
		conn := &boundedMQTTConn{Conn: byteConn{bytes.NewReader(wire)}}
		got, err := io.ReadAll(conn)
		if err != nil || !bytes.Equal(got, wire) {
			t.Fatalf("changed MQTT bytes: %x %v", got, err)
		}
	})
	t.Run("oversized advertised length", func(t *testing.T) {
		conn := &boundedMQTTConn{Conn: byteConn{bytes.NewReader([]byte{0x30, 0xff, 0xff, 0x7f})}}
		got, err := io.ReadAll(conn)
		if err == nil || len(got) != 3 {
			t.Fatalf("released the oversized length: %x %v", got, err)
		}
	})
}
