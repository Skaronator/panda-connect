package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInitializeAndPreserve(t *testing.T) {
	dir := t.TempDir()
	template := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(template, []byte("core: {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	env := func(key string) string {
		return map[string]string{"BAMBU_ADDRESS": "printer:8883", "BAMBU_SERIAL": "TEST", "BAMBU_TLS_FINGERPRINT": strings.Repeat("a", 64)}[key]
	}
	if err := initialize(dir, template, env); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.d", "resources.json")
	before, err := os.ReadFile(path)
	if err != nil || !json.Valid(before) {
		t.Fatal("invalid overlay", err)
	}
	if strings.Contains(string(before), "test-code") || !strings.Contains(string(before), "/run/secrets/bambu_access_code") {
		t.Fatal("overlay contains credentials or omits the secret reference")
	}
	if err = initialize(dir, template, func(string) string { return "" }); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(before) {
		t.Fatal("existing deployment was overwritten")
	}
}

func TestMissingSettingsRefuseInitialization(t *testing.T) {
	template := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(template, []byte("core: {}"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := initialize(t.TempDir(), template, func(string) string { return "" }); err == nil {
		t.Fatal("accepted missing printer settings")
	}
}
