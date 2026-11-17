package bambucamera

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"time"

	"github.com/prusa3d/prusa-connect/sdk/cameraapi"
)

// FFmpeg receives only the authenticated H.264 byte stream over stdin, never
// the printer address or credentials. The HTTP stream needs no ICE or UDP.
func (c *Camera) openMJPEG(ctx context.Context) (cameraapi.Stream, error) {
	source, err := c.openH264(ctx)
	if err != nil {
		return cameraapi.Stream{}, err
	}
	stream, err := c.transcode(source.Body)
	if err != nil {
		return cameraapi.Stream{}, err
	}
	// Return a camera failure, not a 200 with an indefinitely blank tile,
	// if the source is not producing decodable video.
	startup, cancel := context.WithTimeout(ctx, 8*time.Second)
	stop := context.AfterFunc(startup, func() { _ = stream.Body.Close() })
	defer cancel()
	var first [1]byte
	_, err = io.ReadFull(stream.Body, first[:])
	if !stop() || err != nil || startup.Err() != nil {
		_ = stream.Body.Close()
		return cameraapi.Stream{}, errCamera
	}
	stream.Body = &prefixedBody{Reader: io.MultiReader(bytes.NewReader(first[:]), stream.Body), Closer: stream.Body}
	return stream, nil
}

type prefixedBody struct {
	io.Reader
	io.Closer
}

func (c *Camera) transcode(source io.ReadCloser) (cameraapi.Stream, error) {
	input, feed, err := os.Pipe()
	if err != nil {
		_ = source.Close()
		return cameraapi.Stream{}, errCamera
	}
	life, cancel := context.WithCancel(c.life)
	r, w := io.Pipe()
	cmd := exec.CommandContext(life, c.ffmpeg,
		"-hide_banner", "-loglevel", "error", "-nostdin", "-protocol_whitelist", "pipe",
		"-threads", "1", "-max_pixels", "8294400", "-probesize", "65536", "-analyzeduration", "250000",
		"-f", "h264", "-i", "pipe:0", "-an", "-filter_threads", "1",
		"-vf", "fps=5,scale='min(960,iw)':-2", "-threads", "1", "-q:v", "5",
		"-f", "mpjpeg", "-boundary_tag", cameraapi.MJPEGBoundary, "-flush_packets", "1", "pipe:1")
	// An *os.File makes stdin a child-owned descriptor, not an os/exec
	// copier that Wait would have to join before we can close the source.
	cmd.Env = []string{"LC_ALL=C"}
	cmd.Stdin, cmd.Stdout = input, w
	if err := cmd.Start(); err != nil {
		cancel()
		_ = input.Close()
		_ = feed.Close()
		_ = source.Close()
		_ = r.Close()
		_ = w.Close()
		return cameraapi.Stream{}, errCamera
	}
	_ = input.Close()
	go func() { _, _ = io.Copy(feed, source); _ = feed.Close() }()
	stop := context.AfterFunc(life, func() {
		_ = r.Close()
		_ = feed.Close()
		_ = source.Close()
	})
	go func() {
		if err := cmd.Wait(); err != nil {
			_ = w.CloseWithError(errCamera)
		} else {
			_ = w.Close()
		}
		_ = feed.Close()
		_ = source.Close()
		stop()
		cancel()
	}()
	return cameraapi.Stream{Body: &body{ReadCloser: r, cleanup: cancel}, MediaType: cameraapi.FormatMJPEG.MediaType()}, nil
}
