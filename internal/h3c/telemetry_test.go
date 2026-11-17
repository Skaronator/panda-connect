package h3c

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"testing/synctest"
	"time"

	"github.com/prusa3d/prusa-connect/sdk/connectorapi"
)

func writeTestFile(t *testing.T, data string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func patchValues(patches []connectorapi.StatePatch) map[string]string {
	values := make(map[string]string)
	for _, patch := range patches {
		values[patch.Path] = string(patch.Value)
	}
	return values
}

func TestH3CFixture(t *testing.T) {
	var parser telemetry
	patches, full, status, err := parser.parse(fixture(t, "h3c-running.json"))
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
	var parser telemetry
	_, _, _, _ = parser.parse(fixture(t, "h3c-running.json"))
	patches, full, _, err := parser.parse([]byte(`{"print":{"command":"push_status","msg":1,"mc_percent":21}}`))
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
	var parser telemetry
	patches, _, _, err := parser.parse([]byte(`{"print":{"command":"push_status","device":{"bed":{"info":{"temp":4587590}},"extruder":{"info":[{"id":0,"temp":16711935}]}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	values := patchValues(patches)
	if values["/tools/0/nozzle_temp"] != "255" || values["/tools/0/nozzle_target_temp"] != "255" || values["/temperature/bed"] != "70" || values["/temperature/bed_target"] != "70" {
		t.Fatalf("packed temperatures: %v", values)
	}
}

func TestJobLifecycle(t *testing.T) {
	var parser telemetry
	_, _, _, _ = parser.parse(fixture(t, "h3c-running.json"))
	patches, _, _, err := parser.parse([]byte(`{"print":{"command":"push_status","gcode_state":"FINISH","mc_percent":100}}`))
	if err != nil {
		t.Fatal(err)
	}
	if patchValues(patches)["/job/outcome"] != `"finished"` {
		t.Fatalf("outcome: %v", patchValues(patches))
	}
	patches, _, _, err = parser.parse([]byte(`{"print":{"command":"push_status","gcode_state":"RUNNING","task_id":"2286","mc_percent":0}}`))
	if err != nil {
		t.Fatal(err)
	}
	values := patchValues(patches)
	if values["/job/id"] != `"task:2286"` || values["/job/outcome"] != "null" || values["/job/progress"] != "0" {
		t.Fatalf("new job: %v", values)
	}
}

func TestHostStateVocabulary(t *testing.T) {
	for vendor, want := range map[string]string{
		"IDLE": "IDLE", "PREPARE": "BUSY", "RUNNING": "PRINTING", "PAUSE": "PAUSED", "FINISH": "FINISHED", "FAILED": "ERROR", "FUTURE": "UNKNOWN",
	} {
		t.Run(vendor, func(t *testing.T) {
			var parser telemetry
			raw, _ := json.Marshal(map[string]any{"print": map[string]any{"command": "push_status", "task_id": "42", "gcode_state": vendor}})
			patches, _, _, err := parser.parse(raw)
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
	var parser telemetry
	patches, _, _, err := parser.parse([]byte(`{"print":{"command":"push_status","gcode_state":"RUNNING","task_id":"0","mc_percent":10}}`))
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
	var parser telemetry
	patches, _, _, err := parser.parse([]byte(`{"print":{"command":"push_status","gcode_state":"FUTURE_STATE"}}`))
	if err != nil || patchValues(patches)["/state"] != `"UNKNOWN"` {
		t.Fatal("unknown state handling", err)
	}
}

func TestMalformedReports(t *testing.T) {
	for _, frame := range []string{`{`, `{"print":{"command":"push_status","nozzle_temper":{}}}`, `{"print":{"command":"push_status","hms":{}}}`, `{"print":{"command":"push_status","mc_percent":"NaN"}}`} {
		var parser telemetry
		if _, _, _, err := parser.parse([]byte(frame)); err == nil {
			t.Fatalf("accepted %s", frame)
		}
	}
}

func TestBackpressureRetainsOneTimeValues(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newSession(time.Second, time.Minute, "H3C-TEST-001", nil, NewDriver(0).log)
		go s.run()
		defer func() { s.stop(); <-s.exited }()
		s.incoming <- receivedFrame{data: fixture(t, "h3c-running.json"), at: time.Now()}
		synctest.Wait()
		for i := 21; i <= 50; i++ {
			raw, _ := json.Marshal(map[string]any{"print": map[string]any{"command": "push_status", "mc_percent": i}})
			s.incoming <- receivedFrame{data: raw, at: time.Now()}
			synctest.Wait()
		}
		if s.Dropped() == 0 {
			t.Fatal("replaced patches were not counted")
		}
		seen := make(map[string]string)
		for {
			select {
			case patch := <-s.State():
				seen[patch.Path] = string(patch.Value)
			default:
				synctest.Wait()
				select {
				case patch := <-s.State():
					seen[patch.Path] = string(patch.Value)
				default:
					if seen["/job/display"] != `"Test part"` || seen["/job/progress"] != "50" || seen["/tools/0/nozzle_temp"] != "255" {
						t.Fatalf("lost pending state: %v", seen)
					}
					return
				}
			}
		}
	})
}
