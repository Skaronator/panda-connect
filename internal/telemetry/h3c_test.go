package telemetry

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/prusa3d/prusa-connect/sdk/connectorapi"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func patchValues(patches []connectorapi.StatePatch) map[string]string {
	values := make(map[string]string)
	for _, patch := range patches {
		values[patch.Path] = string(patch.Value)
	}
	return values
}

func TestH3CFixture(t *testing.T) {
	var parser H3C
	patches, full, status, err := parser.Parse(fixture(t, "h3c-running.json"))
	if err != nil || !full || !status {
		t.Fatalf("parse: %v full=%v status=%v", err, full, status)
	}
	values := patchValues(patches)
	for path, want := range map[string]string{
		"/state": `"PRINTING"`, "/bambu/gcode_state": `"RUNNING"`, "/job/id": `"task:2285"`, "/job/progress": "20",
		"/job/time_remaining": "4740", "/tools/0/nozzle_temp": "255", "/tools/0/nozzle_target_temp": "255",
		"/temperature/bed": "70", "/temperature/chamber": "47", "/tools/0/features/nozzle_diameter": "0.8", "/bambu/hms": "[]",
	} {
		if values[path] != want {
			t.Errorf("%s=%s, want %s", path, values[path], want)
		}
	}
}

func TestDeltaKeepsUnchangedValues(t *testing.T) {
	var parser H3C
	_, _, _, _ = parser.Parse(fixture(t, "h3c-running.json"))
	patches, full, _, err := parser.Parse([]byte(`{"print":{"command":"push_status","msg":1,"mc_percent":21}}`))
	if err != nil || full {
		t.Fatal("invalid delta handling", err)
	}
	values := patchValues(patches)
	if values["/job/progress"] != "21" {
		t.Fatalf("lost progress: %v", values)
	}
	if _, exists := values["/tools/0/nozzle_temp"]; exists {
		t.Fatal("missing temperature was changed")
	}
	if _, exists := values["/job/id"]; exists {
		t.Fatal("unchanged job was re-created")
	}
}

func TestPackedTemperatures(t *testing.T) {
	var parser H3C
	patches, _, _, err := parser.Parse([]byte(`{"print":{"command":"push_status","device":{"bed":{"info":{"temp":4587590}},"extruder":{"info":[{"id":0,"temp":16711935}]}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	values := patchValues(patches)
	if values["/tools/0/nozzle_temp"] != "255" || values["/tools/0/nozzle_target_temp"] != "255" || values["/temperature/bed"] != "70" || values["/temperature/bed_target"] != "70" {
		t.Fatalf("packed temperatures: %v", values)
	}
}

func TestH3CPackedTemperatureUnavailable(t *testing.T) {
	var parser H3C
	for i, frame := range []string{
		`{"print":{"command":"push_status","device":{"bed":{"info":{"temp":4587590}},"extruder":{"info":[{"id":0,"temp":16711935}]}}}}`,
		`{"print":{"command":"push_status","msg":1,"device":{"bed":{"info":{"temp":65535}},"extruder":{"info":[{"id":0,"temp":65535}]}}}}`,
	} {
		patches, _, _, err := parser.Parse([]byte(frame))
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			continue
		}
		values := patchValues(patches)
		for path, want := range map[string]string{
			"/tools/0/nozzle_temp": "null", "/tools/0/nozzle_target_temp": "0",
			"/bambu/active_nozzle_temp": "null", "/bambu/active_nozzle_target_temp": "0",
			"/temperature/bed": "null", "/temperature/bed_target": "0",
		} {
			if values[path] != want {
				t.Errorf("%s = %q, want %s", path, values[path], want)
			}
		}
	}
}

func TestJobLifecycle(t *testing.T) {
	var parser H3C
	_, _, _, _ = parser.Parse(fixture(t, "h3c-running.json"))
	patches, _, _, err := parser.Parse([]byte(`{"print":{"command":"push_status","gcode_state":"FINISH","mc_percent":100}}`))
	if err != nil {
		t.Fatal(err)
	}
	if patchValues(patches)["/job/outcome"] != `"finished"` {
		t.Fatalf("outcome: %v", patchValues(patches))
	}
	patches, _, _, err = parser.Parse([]byte(`{"print":{"command":"push_status","gcode_state":"RUNNING","task_id":"2286","mc_percent":0}}`))
	if err != nil {
		t.Fatal(err)
	}
	values := patchValues(patches)
	if values["/job/id"] != `"task:2286"` || values["/job/outcome"] != "null" || values["/job/progress"] != "0" {
		t.Fatalf("new job: %v", values)
	}
}

func TestH3CChamberReadings(t *testing.T) {
	var parser H3C
	for _, tc := range []struct {
		wire, current, target, light string
	}{
		{`"msg":0,"device":{"ctc":{"info":{"temp":3932207,"target":55}}},"lights_report":[{"node":"work_light","mode":"flashing"},{"node":"chamber_light","mode":"on"}]`, "47", "55", "100"},
		{`"msg":1,"device":{"ctc":{"info":{"temp":3932207}}}`, "47", "60", ""},
		{`"msg":1,"ctt":0`, "", "0", ""},
		{`"msg":1,"lights_report":[{"node":"work_light","mode":"off"}],"big_fan1_speed":"10"`, "", "", ""},
		{`"msg":1,"device":{"ctc":{"info":{"temp":49,"target":0}}},"lights_report":[{"node":"chamber_light","mode":"off"}]`, "49", "0", "0"},
		{`"msg":1,"device":{"ctc":{"info":{"temp":65535,"target":65535}}},"lights_report":[{"node":"chamber_light","mode":"flashing"}]`, "null", "null", "null"},
		{`"msg":0`, "", "null", "null"},
	} {
		patches, _, _, err := parser.Parse([]byte(`{"print":{"command":"push_status",` + tc.wire + `}}`))
		if err != nil {
			t.Fatal(err)
		}
		values := patchValues(patches)
		for path, want := range map[string]string{
			"/temperature/chamber": tc.current, "/temperature/chamber_target": tc.target,
			"/chamber/led_intensity": tc.light, "/chamber/fan_pwm_target": "",
		} {
			if values[path] != want {
				t.Errorf("%s: %s = %q, want %q", tc.wire, path, values[path], want)
			}
		}
	}
}

func TestHostStateVocabulary(t *testing.T) {
	for vendor, want := range map[string]string{
		"IDLE": "IDLE", "PREPARE": "BUSY", "RUNNING": "PRINTING", "PAUSE": "PAUSED", "FINISH": "FINISHED", "FAILED": "ERROR", "FUTURE": "UNKNOWN",
	} {
		t.Run(vendor, func(t *testing.T) {
			var parser H3C
			raw, _ := json.Marshal(map[string]any{"print": map[string]any{"command": "push_status", "task_id": "42", "gcode_state": vendor}})
			patches, _, _, err := parser.Parse(raw)
			if err != nil {
				t.Fatal(err)
			}
			values := patchValues(patches)
			encoded, _ := json.Marshal(want)
			if values["/state"] != string(encoded) {
				t.Fatalf("%s mapped to %s, want %s", vendor, values["/state"], encoded)
			}
			if vendor == "PREPARE" && values["/job/state"] != `"PRINTING"` {
				t.Fatal("preparation must keep the job open")
			}
		})
	}
}

func TestJobWithoutStableIDIsNotInvented(t *testing.T) {
	var parser H3C
	patches, _, _, err := parser.Parse([]byte(`{"print":{"command":"push_status","gcode_state":"RUNNING","task_id":"0","mc_percent":10}}`))
	if err != nil {
		t.Fatal(err)
	}
	for path := range patchValues(patches) {
		if len(path) >= 5 && path[:5] == "/job/" {
			t.Fatal("invented a job without a stable identity")
		}
	}
}

func TestUnrecognizedStateIsPreservedAsUnknown(t *testing.T) {
	var parser H3C
	patches, _, _, err := parser.Parse([]byte(`{"print":{"command":"push_status","gcode_state":"FUTURE_STATE"}}`))
	if err != nil || patchValues(patches)["/state"] != `"UNKNOWN"` {
		t.Fatal("unknown state handling", err)
	}
}

func TestMalformedReports(t *testing.T) {
	for _, frame := range []string{`{`, `{"print":{"command":"push_status","nozzle_temper":{}}}`, `{"print":{"command":"push_status","hms":{}}}`, `{"print":{"command":"push_status","mc_percent":"NaN"}}`} {
		var parser H3C
		if _, _, _, err := parser.Parse([]byte(frame)); err == nil {
			t.Fatalf("accepted %s", frame)
		}
	}
}
