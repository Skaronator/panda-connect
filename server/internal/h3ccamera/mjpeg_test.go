package h3ccamera

import (
	"bytes"
	"context"
	"image/jpeg"
	"io"
	"mime"
	"mime/multipart"
	"os/exec"
	"testing"
	"time"

	"github.com/prusa3d/prusa-connect/sdk/cameraapi"
)

func TestHTTPMJPEGFrames(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("FFmpeg is not installed")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	encoded, err := exec.CommandContext(ctx, ffmpeg,
		"-v", "error", "-f", "lavfi", "-i", "color=red:size=64x48:rate=10",
		"-frames:v", "20", "-c:v", "libx264", "-threads", "1", "-f", "h264", "pipe:1").Output()
	if err != nil {
		t.Fatal(err)
	}
	c := &Camera{ffmpeg: ffmpeg, life: ctx}
	stream, err := c.transcode(io.NopCloser(bytes.NewReader(encoded)))
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Body.Close()
	_, params, err := mime.ParseMediaType(stream.MediaType)
	if err != nil || params["boundary"] != cameraapi.MJPEGBoundary {
		t.Fatalf("unexpected media type: %q (%v)", stream.MediaType, err)
	}
	parts := multipart.NewReader(stream.Body, params["boundary"])
	for range 2 {
		part, err := parts.NextPart()
		if err != nil {
			t.Fatal(err)
		}
		im, err := jpeg.Decode(part)
		_ = part.Close()
		if err != nil {
			t.Fatal(err)
		}
		if im.Bounds().Dx() != 64 || im.Bounds().Dy() != 48 {
			t.Fatalf("unexpected frame size: %v", im.Bounds())
		}
	}
}

func TestEncoderFailureClosesBlockedInput(t *testing.T) {
	c := &Camera{ffmpeg: "/bin/false", life: t.Context()}
	input, feed := io.Pipe()
	defer feed.Close()
	stream, err := c.transcode(input)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Body.Close()
	done := make(chan error, 1)
	go func() { _, err := io.Copy(io.Discard, stream.Body); done <- err }()
	select {
	case err := <-done:
		if err != errCamera {
			t.Fatalf("unexpected error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("encoder did not release the stream on failure")
	}
}
