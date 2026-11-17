// Package secret reads printer credentials without placing them in errors.
package secret

import (
	"errors"
	"io"
	"os"
	"strings"
)

func Read(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", errors.New("cannot open access-code file")
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 129))
	if err != nil || len(b) > 128 {
		return "", errors.New("invalid secret size")
	}
	code := strings.TrimSpace(string(b))
	if code == "" || strings.ContainsAny(code, "\r\n\x00") {
		return "", errors.New("invalid secret")
	}
	return code, nil
}
