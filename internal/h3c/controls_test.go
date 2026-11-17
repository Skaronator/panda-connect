package h3c

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/prusa3d/prusa-connect/sdk/connectorapi"
	"github.com/prusa3d/prusa-connect/sdk/fault"
)

func connectFake(t *testing.T, p *fakePrinter, timeout time.Duration) *Session {
	t.Helper()
	d := NewDriver(timeout)
	s, err := d.Connect(t.Context(), p.endpoint, d.Baseline())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s.(*Session)
}
func answerControl(p *fakePrinter, c *fakeConnection, cmd map[string]json.RawMessage, result string) {
	raw, _ := json.Marshal(map[string]any{"print": map[string]any{"command": cmd["command"], "sequence_id": cmd["sequence_id"], "result": result}})
	_ = p.sendTo(c, raw)
}
func TestControlsPrinterReplies(t *testing.T) {
	for _, tc := range []struct {
		name        string
		verb        connectorapi.Capability
		args        map[string]string
		wire, param string
	}{
		{"pause", VerbPausePrint, nil, "pause", ""}, {"resume", VerbResumePrint, nil, "resume", ""}, {"stop", VerbStopPrint, nil, "stop", ""},
		{"nozzle", VerbNozzleTemperature, map[string]string{"nozzle_temperature": "350"}, "gcode_line", "M104 S350\n"},
		{"bed", VerbBedTemperature, map[string]string{"bed_temperature": "120"}, "gcode_line", "M140 S120\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := startFake(t)
			if tc.verb == VerbResumePrint {
				p.report = []byte(strings.ReplaceAll(string(p.report), "RUNNING", "PAUSE"))
			}
			p.control = func(c *fakeConnection, cmd map[string]json.RawMessage) {
				var command, param string
				_ = json.Unmarshal(cmd["command"], &command)
				_ = json.Unmarshal(cmd["param"], &param)
				if command != tc.wire || param != tc.param {
					t.Errorf("unexpected control: %s %q", command, param)
				}
				answerControl(p, c, cmd, "success")
			}
			s := connectFake(t, p, time.Second)
			r, err := s.Do(t.Context(), connectorapi.Command{Verb: tc.verb, Args: tc.args})
			if err != nil || !r.OK {
				t.Fatalf("%+v %v", r, err)
			}
		})
	}
}
func TestControlConfirmation(t *testing.T) {
	for _, mode := range []string{"quiet", "wrong sequence", "unknown result", "refused", "observed", "unchanged", "wrong state"} {
		t.Run(mode, func(t *testing.T) {
			p := startFake(t)
			p.control = func(c *fakeConnection, cmd map[string]json.RawMessage) {
				switch mode {
				case "wrong sequence":
					cmd["sequence_id"] = json.RawMessage(`"999999"`)
					answerControl(p, c, cmd, "success")
				case "unknown result":
					answerControl(p, c, cmd, "maybe")
				case "refused":
					answerControl(p, c, cmd, "fail")
				case "observed":
					_ = p.sendTo(c, []byte(`{"print":{"command":"push_status","gcode_state":"PAUSE"}}`))
				case "unchanged":
					_ = p.sendTo(c, []byte(`{"print":{"command":"push_status","bed_target_temper":70}}`))
				case "wrong state":
					_ = p.sendTo(c, []byte(`{"print":{"command":"push_status","gcode_state":"FINISH"}}`))
				}
			}
			s := connectFake(t, p, 120*time.Millisecond)
			cmd := connectorapi.Command{Verb: VerbPausePrint}
			if mode == "unchanged" {
				cmd = connectorapi.Command{Verb: VerbBedTemperature, Args: map[string]string{"bed_temperature": "70"}}
			}
			r, err := s.Do(t.Context(), cmd)
			switch mode {
			case "refused":
				if err != nil || r.OK || r.FaultCode != CommandRefused.Code().String() {
					t.Fatalf("refusal: %+v %v", r, err)
				}
			case "observed":
				if err != nil || !r.OK {
					t.Fatalf("observation: %+v %v", r, err)
				}
			default:
				if !errors.Is(err, context.DeadlineExceeded) || r.OK {
					t.Fatalf("unconfirmed command: %+v %v", r, err)
				}
			}
		})
	}
}
func TestControlConcurrencyAndCancellation(t *testing.T) {
	for _, end := range []string{"cancel", "disconnect", "close"} {
		t.Run(end, func(t *testing.T) {
			p := startFake(t)
			received := make(chan struct{}, 1)
			p.control = func(_ *fakeConnection, _ map[string]json.RawMessage) { received <- struct{}{} }
			s := connectFake(t, p, time.Second)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			finished := make(chan error, 1)
			go func() { _, err := s.Do(ctx, connectorapi.Command{Verb: VerbPausePrint}); finished <- err }()
			select {
			case <-received:
			case <-time.After(time.Second):
				t.Fatal("no command")
			}
			if _, err := s.Do(t.Context(), connectorapi.Command{Verb: VerbStopPrint}); !fault.HasCode(err, CommandBusy.Code()) {
				t.Fatalf("concurrent request: %v", err)
			}
			switch end {
			case "cancel":
				cancel()
			case "disconnect":
				p.disconnect()
			case "close":
				_ = s.Close()
			}
			select {
			case err := <-finished:
				if err == nil {
					t.Fatal("pending control succeeded")
				}
				if end == "cancel" && !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("pending control leaked")
			}
		})
	}
}
func TestControlArgumentsAndStateGuards(t *testing.T) {
	p := startFake(t)
	s := connectFake(t, p, time.Second)
	before := p.requests.Load()
	for _, cmd := range []connectorapi.Command{
		{Verb: VerbNozzleTemperature, Args: map[string]string{"nozzle_temperature": "351"}},
		{Verb: VerbBedTemperature, Args: map[string]string{"bed_temperature": "121"}},
		{Verb: VerbBedTemperature, Args: map[string]string{"bed_temperature": "-1"}},
		{Verb: VerbBedTemperature, Args: map[string]string{"bed_temperature": "NaN"}},
		{Verb: VerbNozzleTemperature, Args: map[string]string{"nozzle_temperature": "200\nG28"}},
		{Verb: VerbNozzleTemperature, Args: map[string]string{"nozzle_temperature": "200", "tool": "1"}},
		{Verb: VerbNozzleTemperature},
	} {
		if _, err := s.Do(t.Context(), cmd); err == nil {
			t.Fatalf("accepted %+v", cmd)
		}
	}
	if r, err := s.Do(t.Context(), connectorapi.Command{Verb: VerbResumePrint}); err != nil || r.OK || r.FaultCode == "" {
		t.Fatalf("state guard: %+v %v", r, err)
	}
	if p.requests.Load() != before {
		t.Fatal("invalid or state-refused control reached printer")
	}
}
