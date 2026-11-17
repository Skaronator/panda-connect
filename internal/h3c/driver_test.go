package h3c

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/prusa3d/prusa-connect/sdk/connectorapi"
	"github.com/prusa3d/prusa-connect/sdk/connectorapi/connectorapitest"
	"github.com/prusa3d/prusa-connect/sdk/extension"
	"github.com/prusa3d/prusa-connect/sdk/fault"
)

func TestContract(t *testing.T) {
	connectorapitest.RunDriverContract(t, func(t *testing.T) connectorapitest.Subject {
		printer := startFake(t)
		return connectorapitest.Subject{Driver: NewDriver(time.Second), Endpoint: printer.endpoint, Connectable: true, SafeVerb: VerbSendStateInfo}
	})
}

func TestRegistration(t *testing.T) {
	namespace, err := extension.NamespaceOf("github.com/skaronator/pandalink")
	if err != nil || namespace.String() != "com.github.aux.skaronator.pandalink" {
		t.Fatalf("namespace: %s, %v", namespace, err)
	}
	if err = connectorapi.ValidateDialect(dialect()); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, d := range connectorapi.Descriptors() {
		if d.Name() == Type.String() {
			found = true
		}
	}
	if !found {
		t.Fatal("PandaLink is not registered")
	}
	for _, option := range Doc().Options {
		if option.Key == "timeout" && option.Default != DefaultTimeout.String() {
			t.Fatal("documented timeout differs")
		}
	}
}

func TestConfig(t *testing.T) {
	for _, raw := range []string{`{"timeout":"bad"}`, `{"timeout":"0s"}`, `{"stale_after":"-1s"}`, `{"timeout":12}`} {
		if _, err := New(connectorapi.Spec{Config: json.RawMessage(raw)}); !fault.HasCode(err, InvalidConfig.Code()) {
			t.Fatalf("accepted %s: %v", raw, err)
		}
	}
	if _, err := New(connectorapi.Spec{Config: json.RawMessage(`{"timeout":"1s","stale_after":"3s"}`)}); err != nil {
		t.Fatal(err)
	}
}

func TestConnectionSecurity(t *testing.T) {
	t.Run("pin mismatch", func(t *testing.T) {
		p := startFake(t)
		p.endpoint.Options["tls_fingerprint"] = strings.Repeat("0", 64)
		_, err := NewDriver(time.Second).Connect(t.Context(), p.endpoint, NewDriver(0).Baseline())
		if err == nil {
			t.Fatal("accepted the wrong certificate")
		}
	})
	t.Run("serial mismatch", func(t *testing.T) {
		_, found, err := decodeIdentity(fixture(t, "h3c-version.json"), "WRONG")
		if !found || err == nil {
			t.Fatal("accepted the wrong serial")
		}
	})
	t.Run("missing pin", func(t *testing.T) {
		if _, err := printerTLS(nil); err == nil {
			t.Fatal("allowed unverified TLS")
		}
	})
	t.Run("wrong password", func(t *testing.T) {
		p := startFake(t)
		p.endpoint.Options["access_code_file"] = writeTestFile(t, "wrong")
		_, err := NewDriver(time.Second).Connect(t.Context(), p.endpoint, NewDriver(0).Baseline())
		if err == nil || strings.Contains(err.Error(), "wrong") {
			t.Fatalf("bad authentication handling: %v", err)
		}
	})
}

func TestMonitoringAndReconnect(t *testing.T) {
	p := startFake(t)
	d := NewDriver(time.Second)
	connect := func() connectorapi.Session {
		s, err := d.Connect(t.Context(), p.endpoint, d.Baseline())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
		return s
	}
	s := connect()
	id := s.Identity()
	state := readUntil(t, s.State(), "/temperature/chamber")
	if string(state["/tools/0/nozzle_temp"]) != "255" || string(state["/job/id"]) != `"task:2285"` {
		t.Fatalf("wrong state: %s", state)
	}
	p.disconnect()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	for {
		select {
		case _, open := <-s.State():
			if !open {
				goto closed
			}
		case <-ctx.Done():
			t.Fatal("disconnect did not close the state stream")
		}
	}
closed:
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s = connect()
	if s.Identity() != id {
		t.Fatal("printer identity changed after reconnect")
	}
	state = readUntil(t, s.State(), "/job/id")
	if string(state["/job/id"]) != `"task:2285"` {
		t.Fatal("job identity changed after reconnect")
	}
}

func TestRefreshNeedsAnObservedReport(t *testing.T) {
	p := startFake(t)
	d := NewDriver(150 * time.Millisecond)
	s, err := d.Connect(t.Context(), p.endpoint, d.Baseline())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p.quiet.Store(true)
	result, err := s.Do(t.Context(), connectorapi.Command{Verb: VerbSendStateInfo})
	if err == nil || result.OK {
		t.Fatal("MQTT delivery without a printer report was called success")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	result, err = s.Do(ctx, connectorapi.Command{Verb: VerbSendStateInfo})
	if !errors.Is(err, context.Canceled) || result.OK {
		t.Fatal("cancelled refresh succeeded")
	}
}

func TestSessionOutlivesConnectContext(t *testing.T) {
	p := startFake(t)
	d := NewDriver(time.Second)
	ctx, cancel := context.WithCancel(t.Context())
	s, err := d.Connect(ctx, p.endpoint, d.Baseline())
	cancel()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	result, err := s.Do(t.Context(), connectorapi.Command{Verb: VerbSendStateInfo})
	if err != nil || !result.OK {
		t.Fatalf("Connect context ended the session: %+v %v", result, err)
	}
}

func TestConcurrentRefreshAndClose(t *testing.T) {
	p := startFake(t)
	d := NewDriver(time.Second)
	s, err := d.Connect(t.Context(), p.endpoint, d.Baseline())
	if err != nil {
		t.Fatal(err)
	}
	p.quiet.Store(true)
	finished := make(chan struct{})
	go func() { defer close(finished); _, _ = s.Do(t.Context(), connectorapi.Command{Verb: VerbSendStateInfo}) }()
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not release a pending refresh")
	}
}

func TestStaleSessionEnds(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newSession(time.Second, 2*time.Second, "H3C-TEST-001", nil, NewDriver(0).log)
		go s.run()
		time.Sleep(3 * time.Second)
		synctest.Wait()
		select {
		case _, open := <-s.State():
			if open {
				t.Fatal("stale stream is open")
			}
		default:
			t.Fatal("stale stream is not closed")
		}
	})
}

func readUntil(t *testing.T, stream <-chan connectorapi.StatePatch, path string) map[string]json.RawMessage {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	values := make(map[string]json.RawMessage)
	for {
		select {
		case patch, open := <-stream:
			if !open {
				t.Fatalf("stream ended before %s", path)
			}
			values[patch.Path] = patch.Value
			if patch.Path == path {
				return values
			}
		case <-ctx.Done():
			t.Fatalf("timed out waiting for %s", path)
		}
	}
}
