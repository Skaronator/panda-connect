// pandalink-init seeds a new local-test deployment. Existing files are never replaced.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

func main() {
	data := flag.String("data-dir", "/data", "directory holding this deployment")
	template := flag.String("template", "/etc/pandalink/config.yaml", "initial server config")
	flag.Parse()
	if err := initialize(*data, *template, os.Getenv); err != nil {
		fmt.Fprintln(os.Stderr, "pandalink-init:", err)
		os.Exit(1)
	}
}

func initialize(data, template string, getenv func(string) string) error {
	if err := os.MkdirAll(filepath.Join(data, "config.d"), 0700); err != nil {
		return err
	}
	config, err := os.ReadFile(template)
	if err != nil {
		return err
	}
	if err = writeNew(filepath.Join(data, "config.yaml"), config); err != nil {
		return err
	}
	path := filepath.Join(data, "config.d", "resources.json")
	if _, err = os.Stat(path); err == nil {
		fmt.Println("PandaLink: keeping existing deployment and endpoints; use the endpoint API to change them.")
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	address, serial, pin := getenv("BAMBU_ADDRESS"), getenv("BAMBU_SERIAL"), getenv("BAMBU_TLS_FINGERPRINT")
	if address == "" || serial == "" || pin == "" {
		return errors.New("set BAMBU_ADDRESS, BAMBU_SERIAL and BAMBU_TLS_FINGERPRINT in .env")
	}
	// This initial overlay is written only before the server's first start.
	// Once started, the host owns it and all changes go through its API.
	overlay := map[string]any{
		"version": 1, "instances": []any{},
		"auth": map[string]string{"mode": "open"}, "admission": map[string]string{"mode": "open"},
		"endpoints": []any{map[string]any{
			"name": "bambu-printer", "connector": "pandalink", "address": address, "enabled": true,
			"options": map[string]string{"serial": serial, "access_code_file": "/run/secrets/bambu_access_code", "tls_fingerprint": pin},
		}},
	}
	raw, err := json.MarshalIndent(overlay, "", "  ")
	if err != nil {
		return err
	}
	if err = writeNew(path, append(raw, '\n')); err != nil {
		return err
	}
	fmt.Println("PandaLink: initialized loopback-only local test deployment without login.")
	return nil
}

func writeNew(path string, content []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if errors.Is(err, os.ErrExist) {
		return nil
	}
	if err != nil {
		return err
	}
	_, writeErr := f.Write(content)
	return errors.Join(writeErr, f.Close())
}
