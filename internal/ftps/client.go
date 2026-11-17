// Package ftps implements a restricted implicit-FTPS client. No active FTP, arbitrary
// PASV hosts, cleartext data, URLs, or protocol debug logging are supported.
package ftps

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

var errFTP = errors.New("printer file protocol failed")

// Client is one authenticated FTPS control connection. Operations are serial.
type Client struct {
	conn   *tls.Conn
	reader *bufio.Reader
	cfg    *tls.Config
	ctx    context.Context
	mu     sync.Mutex
	data   net.Conn
	closed bool
	stop   func() bool
}

// Dial authenticates and requires TLS on the control and data connections.
func Dial(ctx context.Context, address, code string, cfg *tls.Config) (*Client, error) {
	cfg = cfg.Clone()
	host, _, _ := net.SplitHostPort(address)
	if cfg.ServerName == "" {
		cfg.ServerName = host
	}
	// Go uses ServerName as the cache key, so data connections on different
	// passive ports can resume the authenticated control channel's TLS session.
	cfg.ClientSessionCache = tls.NewLRUClientSessionCache(1)
	c, err := (&tls.Dialer{Config: cfg, NetDialer: &net.Dialer{Timeout: 15 * time.Second}}).DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, errFTP
	}
	f := &Client{conn: c.(*tls.Conn), cfg: cfg, ctx: ctx}
	f.reader = bufio.NewReaderSize(c, 8192)
	if deadline, ok := ctx.Deadline(); ok {
		_ = c.SetDeadline(deadline)
	}
	f.stop = context.AfterFunc(ctx, f.closeSockets)
	ok := false
	defer func() {
		if !ok {
			f.Close()
		}
	}()
	if _, err = f.response(220); err != nil {
		return nil, err
	}
	if _, err = f.command(331, "USER bblp"); err != nil {
		return nil, err
	}
	if _, err = f.command(230, "PASS "+code); err != nil {
		return nil, err
	}
	for _, command := range []string{"PBSZ 0", "PROT P", "TYPE I"} {
		if _, err = f.command(200, command); err != nil {
			return nil, err
		}
	}
	ok = true
	return f, nil
}

func (f *Client) closeSockets() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	_ = f.conn.Close()
	if f.data != nil {
		_ = f.data.Close()
	}
}

// Close closes the control connection and any pending data connection.
func (f *Client) Close() {
	if f.stop != nil {
		f.stop()
	}
	f.closeSockets()
}

func (f *Client) response(want int) (string, error) {
	var first string
	for i := 0; i < 128; i++ {
		line, err := f.reader.ReadSlice('\n')
		if err != nil || len(line) < 5 {
			return "", errFTP
		}
		text := strings.TrimSuffix(strings.TrimSuffix(string(line), "\n"), "\r")
		if len(text) < 4 {
			return "", errFTP
		}
		if i == 0 {
			first = text[:3]
		}
		if text[:3] != first || text[3] != ' ' {
			continue
		}
		code, err := strconv.Atoi(first)
		if err != nil || code != want {
			return "", errFTP
		}
		return text[4:], nil
	}
	return "", errFTP
}
func (f *Client) command(want int, command string) (string, error) {
	if strings.ContainsAny(command, "\r\n\x00") {
		return "", errFTP
	}
	if err := f.ctx.Err(); err != nil {
		return "", err
	}
	if _, err := io.WriteString(f.conn, command+"\r\n"); err != nil {
		return "", errFTP
	}
	return f.response(want)
}

func (f *Client) passive() (net.Conn, error) {
	answer, err := f.command(229, "EPSV")
	port := 0
	if err == nil {
		start, end := strings.Index(answer, "("), strings.Index(answer, ")")
		if start < 0 || end <= start {
			return nil, errFTP
		}
		parts := strings.Split(answer[start+1:end], "|")
		if len(parts) != 5 {
			return nil, errFTP
		}
		port, err = strconv.Atoi(parts[3])
	} else {
		answer, err = f.command(227, "PASV")
		if err != nil {
			return nil, err
		}
		start, end := strings.Index(answer, "("), strings.Index(answer, ")")
		if start < 0 || end <= start {
			return nil, errFTP
		}
		parts := strings.Split(answer[start+1:end], ",")
		if len(parts) != 6 {
			return nil, errFTP
		}
		a, e1 := strconv.Atoi(parts[4])
		b, e2 := strconv.Atoi(parts[5])
		if e1 != nil || e2 != nil || a < 0 || a > 255 || b < 0 || b > 255 {
			return nil, errFTP
		}
		port = a*256 + b
	}
	if err != nil || port < 1 || port > 65535 {
		return nil, errFTP
	}
	// Ignore the PASV address: never dial a third party named by a report.
	peer := f.conn.RemoteAddr().(*net.TCPAddr).IP.String()
	c, err := (&net.Dialer{Timeout: 15 * time.Second}).DialContext(f.ctx, "tcp", net.JoinHostPort(peer, strconv.Itoa(port)))
	if err != nil {
		return nil, errFTP
	}
	if deadline, ok := f.ctx.Deadline(); ok {
		_ = c.SetDeadline(deadline)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		_ = c.Close()
		return nil, errFTP
	}
	f.data = c
	return c, nil
}

func (f *Client) transfer(command string, copyData func(io.ReadWriter) error) error {
	if strings.ContainsAny(command, "\r\n\x00") {
		return errFTP
	}
	c, err := f.passive()
	if err != nil {
		return err
	}
	defer c.Close()
	if err := f.ctx.Err(); err != nil {
		return err
	}
	// vsftpd expects the transfer request before the data TLS handshake.
	if _, err = io.WriteString(f.conn, command+"\r\n"); err != nil {
		return errFTP
	}
	if _, err = f.response(150); err != nil {
		return err
	}
	secure := tls.Client(c, f.cfg)
	if err = secure.HandshakeContext(f.ctx); err != nil {
		return errFTP
	}
	err = copyData(secure)
	closeErr := secure.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return errFTP
	}
	_, err = f.response(226)
	return err
}

// Store writes to a remote path. The caller must check for collisions first.
func (f *Client) Store(path string, write func(io.Writer) error) error {
	return f.transfer("STOR "+path, func(data io.ReadWriter) error { return write(data) })
}

// Retrieve reads from a remote path until the callback returns.
func (f *Client) Retrieve(path string, read func(io.Reader) error) error {
	return f.transfer("RETR "+path, func(data io.ReadWriter) error { return read(data) })
}

// Entry describes a Unix LIST record. Other entries are kept only for
// collision checks; they are not regular files or traversable directories.
type Entry struct {
	Name          string
	Size          int64
	Folder, Other bool
}

// List reads a bounded Unix directory listing from the printer.
func (f *Client) List(path string) ([]Entry, error) {
	var entries []Entry
	err := f.transfer("LIST "+path, func(c io.ReadWriter) error {
		limited := &io.LimitedReader{R: c, N: (4 << 20) + 1}
		scanner := bufio.NewScanner(limited)
		for scanner.Scan() {
			if len(entries) >= 10000 {
				return errFTP
			}
			line := scanner.Text()
			parts := strings.Fields(line)
			if len(parts) == 2 && parts[0] == "total" {
				continue
			}
			if len(parts) < 9 || len(line) == 0 {
				return errFTP
			}
			// Fields 0..7 are the Unix LIST metadata; preserve filename spaces.
			name := line
			for i := 0; i < 8; i++ {
				name = strings.TrimLeft(name, " \t")
				at := strings.IndexAny(name, " \t")
				if at < 0 {
					return errFTP
				}
				name = name[at:]
			}
			name = strings.TrimLeft(name, " \t")
			if line[0] == 'l' {
				// Keep links for collision checks, never expose them for traversal.
				name, _, _ = strings.Cut(name, " -> ")
			}
			if name == "." || name == ".." {
				continue
			}
			if !ValidSegment(name) {
				return errFTP
			}
			size, err := strconv.ParseInt(parts[4], 10, 64)
			if err != nil || size < 0 {
				return errFTP
			}
			entries = append(entries, Entry{Name: name, Size: size, Folder: line[0] == 'd', Other: line[0] != 'd' && line[0] != '-'})
		}
		if limited.N == 0 {
			return errFTP
		}
		return scanner.Err()
	})
	return entries, err
}

// ValidSegment checks a single remote filename or directory name.
func ValidSegment(s string) bool {
	return s != "" && s != "." && s != ".." && len(s) <= 255 && utf8.ValidString(s) &&
		!strings.ContainsAny(s, "/\\%:;?*\"<>|") && !strings.ContainsFunc(s, unicode.IsControl) && strings.TrimSpace(s) == s
}
