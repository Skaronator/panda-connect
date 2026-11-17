package bambu

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/prusa3d/prusa-connect/sdk/connectorapi"
	"github.com/skaronator/pandalink/internal/ftps"
)

const storageName = "sdcard"
const maxUploadBytes = 2 << 30

type storageAccess struct {
	address, secretFile string
	tls                 *tls.Config
	uploads             bool
}

var (
	_ connectorapi.Filesystem = (*Session)(nil)
	_ connectorapi.Transfers  = (*Session)(nil)
)

func storagePath(p string, root bool) (string, error) {
	if err := connectorapi.RefusePathEscape(p); err != nil {
		return "", err
	}
	parts := strings.Split(p, "/")
	if len(p) > 1024 || len(parts) == 0 || parts[0] != storageName || (!root && len(parts) < 2) {
		return "", connectorapi.PathNotAllowed.New("use a storage-relative sdcard path")
	}
	for _, s := range parts {
		if !ftps.ValidSegment(s) {
			return "", connectorapi.PathNotAllowed.New("path contains an unsafe segment")
		}
	}
	return "/" + strings.Join(parts[1:], "/"), nil
}

func (s *Session) storageContext(ctx context.Context, duration time.Duration) (context.Context, func()) {
	ctx, cancel := context.WithTimeout(ctx, duration)
	stop := context.AfterFunc(s.life, cancel)
	return ctx, func() { stop(); cancel() }
}
func (s *Session) openStorage(ctx context.Context) (*ftps.Client, error) {
	if s.storage == nil {
		return nil, connectorapi.ErrNoFilesystem
	}
	code, err := readSecret(s.storage.secretFile)
	if err != nil {
		return nil, StorageFailed.New("cannot read printer access-code file")
	}
	f, err := ftps.Dial(ctx, s.storage.address, code, s.storage.tls)
	if err != nil {
		return nil, StorageFailed.New("cannot authenticate a verified FTPS connection")
	}
	return f, nil
}

func (s *Session) Storages(ctx context.Context) ([]connectorapi.Storage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.storage == nil || !s.storageSeen.Load() {
		return []connectorapi.Storage{}, nil
	}
	return []connectorapi.Storage{{Mountpoint: "/sdcard", Name: storageName, Type: "Bambu local storage"}}, nil
}

// The released host discovers this optional report interface structurally.
// A configured mount is not evidence that the printer's FTPS root is readable.
func (s *Session) AskStorages(ctx context.Context) error {
	_, err := s.List(ctx, storageName)
	return err
}
func (s *Session) StoragesReported() bool           { return s.storageSeen.Load() }
func (s *Session) StoragesChanged() <-chan struct{} { return s.storageChanged }

func (s *Session) List(ctx context.Context, p string) (connectorapi.Listing, error) {
	if !s.caps.Has(connectorapi.VerbSendFileInfo) {
		return connectorapi.Listing{}, connectorapi.ErrNoFilesystem
	}
	if p == "" {
		if !s.storageSeen.Load() {
			if err := s.AskStorages(ctx); err != nil {
				return connectorapi.Listing{}, err
			}
		}
		return connectorapi.Listing{Directory: connectorapi.File{Folder: true}, Entries: []connectorapi.File{{Path: storageName, Name: storageName, Folder: true}}}, ctx.Err()
	}
	remote, err := storagePath(p, true)
	if err != nil {
		return connectorapi.Listing{}, err
	}
	select {
	case s.transferLock <- struct{}{}:
		defer func() { <-s.transferLock }()
	default:
		return connectorapi.Listing{}, connectorapi.ErrTransferBusy
	}
	ctx, cancel := s.storageContext(ctx, s.timeout)
	defer cancel()
	f, err := s.openStorage(ctx)
	if err != nil {
		return connectorapi.Listing{}, err
	}
	defer f.Close()
	l, err := listStorage(f, p, remote)
	if err == nil && p == storageName && !s.storageSeen.Swap(true) {
		select {
		case s.storageChanged <- struct{}{}:
		default:
		}
	}
	return l, err
}

// Validate each ancestor as a listed directory. LIST does not expose links as
// directories. No client-side check can protect against concurrent renames.
func listVerifiedDir(f *ftps.Client, remote string) ([]ftps.Entry, error) {
	dir := "/"
	if remote != "/" {
		for _, segment := range strings.Split(strings.TrimPrefix(remote, "/"), "/") {
			entries, err := f.List(dir)
			if err != nil {
				return nil, err
			}
			found := false
			for _, e := range entries {
				if e.Name == segment && e.Folder {
					found = true
					break
				}
			}
			if !found {
				return nil, errors.New("printer directory was not listed")
			}
			dir = path.Join(dir, segment)
		}
	}
	return f.List(dir)
}

func listStorage(f *ftps.Client, p, remote string) (connectorapi.Listing, error) {
	if p != storageName {
		entries, err := listVerifiedDir(f, path.Dir(remote))
		if err != nil {
			return connectorapi.Listing{}, StorageFailed.New("cannot list parent directory")
		}
		found := false
		for _, e := range entries {
			if e.Name == path.Base(remote) && !e.Other {
				found = true
				if !e.Folder {
					return connectorapi.Listing{Directory: connectorapi.File{Path: p, Name: e.Name, Size: e.Size}}, nil
				}
				break
			}
		}
		if !found {
			return connectorapi.Listing{}, StorageFailed.New("path not found on printer")
		}
	}
	entries, err := f.List(remote)
	if err != nil {
		return connectorapi.Listing{}, StorageFailed.New("cannot list printer directory")
	}
	l := connectorapi.Listing{Directory: connectorapi.File{Path: p, Name: path.Base(p), Folder: true}}
	for _, e := range entries {
		if e.Other {
			continue
		}
		l.Entries = append(l.Entries, connectorapi.File{Path: p + "/" + e.Name, Name: e.Name, Folder: e.Folder, Size: e.Size})
	}
	return l, nil
}

func (s *Session) Send(ctx context.Context, up connectorapi.Upload) error {
	remote, err := storagePath(up.Path, false)
	if err != nil {
		return err
	}
	// Plain LIST can omit hidden files, so it cannot protect their names.
	if strings.HasPrefix(path.Base(remote), ".") {
		return connectorapi.PathNotAllowed.New("hidden upload destinations are not supported")
	}
	if !s.caps.Has(connectorapi.VerbFileUpload) {
		return connectorapi.ErrNoTransfers
	}
	if up.Content == nil || up.Content.Size() < 0 || up.Content.Size() > maxUploadBytes {
		return StorageFailed.New("upload size must be known and no larger than 2 GiB")
	}
	select {
	case s.transferLock <- struct{}{}:
		defer func() { <-s.transferLock }()
	default:
		return connectorapi.ErrTransferBusy
	}
	ctx, cancel := s.storageContext(ctx, 10*time.Minute)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	f, err := s.openStorage(ctx)
	if err != nil {
		return err
	}
	defer f.Close()
	entries, err := listVerifiedDir(f, path.Dir(remote))
	if err != nil {
		return StorageFailed.New("cannot inspect upload destination")
	}
	for _, e := range entries {
		if strings.EqualFold(e.Name, path.Base(remote)) {
			return connectorapi.ErrTransferRefused
		}
	}
	r, err := up.Content.Open(ctx, 0)
	if err != nil {
		return StorageFailed.New("cannot open upload content")
	}
	defer r.Close()
	stop := context.AfterFunc(ctx, func() { _ = r.Close() })
	defer stop()
	total := up.Content.Size()
	progress := connectorapi.Progress{Total: total}
	emit := func() {
		if up.Progress != nil {
			up.Progress(progress)
		}
	}
	err = f.Store(remote, func(data io.Writer) error {
		emit()
		buf := make([]byte, 64<<10)
		for progress.Sent < total {
			if err := ctx.Err(); err != nil {
				return err
			}
			n, err := r.Read(buf[:min(int64(len(buf)), total-progress.Sent)])
			if n > 0 {
				written, e := data.Write(buf[:n])
				progress.Sent += int64(written)
				emit()
				if e != nil {
					return e
				}
				if written != n {
					return io.ErrShortWrite
				}
			}
			if err != nil {
				if err == io.EOF && progress.Sent == total {
					break
				}
				return io.ErrUnexpectedEOF
			}
			if n == 0 {
				return io.ErrNoProgress
			}
		}
		return nil
	})
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.Join(connectorapi.ErrTransferFailed, StorageFailed.New("upload did not finish; a partial file may remain"))
	}
	entries, err = f.List(path.Dir(remote))
	if err != nil {
		return connectorapi.TransferNotOnStorage.New("cannot verify uploaded file")
	}
	for _, e := range entries {
		if e.Name == path.Base(remote) && !e.Folder && !e.Other && e.Size == total {
			progress.Known = true
			progress.Received = total
			emit()
			return nil
		}
	}
	return connectorapi.TransferNotOnStorage.New("uploaded filename or size differs from the offer")
}

func storageEndpoint(address string, options map[string]string, cfg *tls.Config) (*storageAccess, error) {
	host, _, _ := net.SplitHostPort(address)
	port := "990"
	if p := options["ftps_port"]; p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 65535 {
			return nil, InvalidConfig.New("invalid ftps_port")
		}
		port = p
	}
	upload := options["allow_uploads"]
	if upload != "" && upload != "true" && upload != "false" {
		return nil, InvalidConfig.New("allow_uploads must be true or false")
	}
	return &storageAccess{address: net.JoinHostPort(host, port), secretFile: options["access_code_file"], tls: cfg.Clone(), uploads: upload == "true"}, nil
}
