package h3c

import (
	"errors"
	"net"
)

// Paho allocates from MQTT's advertised remaining length before its callback.
// Check the fixed header before releasing its last length byte to the library.
// Only Paho's single reader touches this state; writes go straight to TLS.
type boundedMQTTConn struct {
	net.Conn
	body          int
	length        int
	digits        int
	readingLength bool
}

func (c *boundedMQTTConn) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if c.body > 0 {
		n, err := c.Conn.Read(p[:min(len(p), c.body)])
		c.body -= n
		return n, err
	}
	n, err := c.Conn.Read(p[:1])
	if n == 0 {
		return n, err
	}
	if !c.readingLength {
		c.readingLength = true
		c.length, c.digits = 0, 0
		return n, err
	}
	c.length |= int(p[0]&127) << (7 * c.digits)
	c.digits++
	if c.length > maxFrameBytes+256 || c.digits == 4 && p[0]&128 != 0 {
		return 0, errors.New("MQTT frame exceeds PandaLink's limit or has an invalid length")
	}
	if p[0]&128 == 0 {
		c.body = c.length
		c.readingLength = false
	}
	return n, err
}
