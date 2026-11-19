package telemetry

import (
	"encoding/json"
	"testing"
)

func TestInventoryAndStableHomeIDs(t *testing.T) {
	var parser H3C
	patches, _, _, err := parser.Parse(fixture(t, "h3c-inventory.json"))
	if err != nil {
		t.Fatal(err)
	}
	values := patchValues(patches)
	for p, want := range map[string]string{
		"/tool_count": "9", "/tools/0/enabled": "true", "/tools/8/active": "true", "/tools/0/active": "false",
		"/bambu/active_tool": `"8"`, "/tools/8/nozzle_temp": "205", "/tools/8/nozzle_target_temp": "220",
		"/tools/0/features/nozzle_high_flow": "true", "/tools/0/features/nozzle_hardened": "true",
		"/bambu/printtools/0/source": `"ams:32:0"`, "/bambu/printtools/8/source": `"ams:32:8"`,
		"/tools/0.0/material_package_instance/package/material/type": `"PLA"`,
		"/tools/8.0/material_package_instance/package/material/type": `"PETG"`,
		"/bambu/ams/32/slots/7/present":                              "false", "/bambu/ams/32/zones/0/humidity": "25",
	} {
		if values[p] != want {
			t.Errorf("%s = %s want %s", p, values[p], want)
		}
	}
	patches, _, _, err = parser.Parse([]byte(`{"print":{"command":"push_status","device":{"nozzle":{"exist":33423361,"info":[{"id":24,"addr_id":8},{"id":0,"addr_id":0}]}},"nozzle_temper":30,"nozzle_target_temper":0}}`))
	if err != nil {
		t.Fatal(err)
	}
	values = patchValues(patches)
	if values["/bambu/active_tool"] != `"0"` || values["/tools/8/active"] != "false" || values["/tools/0/active"] != "true" {
		t.Fatalf("tool swap: %v", values)
	}
	if _, exists := values["/tools/0/features/nozzle_diameter"]; exists {
		t.Fatal("swap changed home-address nozzle identity")
	}
	if _, exists := values["/tools/0/nozzle_temp"]; exists {
		t.Fatal("legacy scalar overwrote H3C inventory")
	}
}
func TestInventoryDeltaRemovalAndReconnect(t *testing.T) {
	var parser H3C
	_, _, _, _ = parser.Parse(fixture(t, "h3c-inventory.json"))
	patches, _, _, err := parser.Parse([]byte(`{"print":{"command":"push_status","ams":{"tray_exist_bits":"1000000000","ams":[{"id":"32","zone_info":[{"zone_id":0,"temp":"60"}]}]},"device":{"nozzle":{"hc":255}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	values := patchValues(patches)
	for p, want := range map[string]string{"/tools/8/enabled": "false", "/tools/8/features/nozzle_diameter": "null", "/tools/8.0/material_package_instance/package/material/type": "null", "/tools/0.0/material_package_instance/package/material/type": "null", "/tools/0.0/material_package_instance/package/material/primary_color/color_rgba": "null", "/bambu/ams/32/zones/0/temperature": "60"} {
		if values[p] != want {
			t.Errorf("%s=%s want %s", p, values[p], want)
		}
	}
	if _, exists := values["/bambu/ams/32/zones/0/humidity"]; exists {
		t.Fatal("partial zone update changed humidity")
	}
	patches, _, _, err = parser.Parse([]byte(`{"print":{"command":"push_status","ams":{"tray_exist_bits":"1010000000","ams":[{"id":"32","tray":[{"id":"0","tray_type":"ABS"}]}]}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if raw := parser.inventory.previous["/tools/0.0/material_package_instance/package/material/primary_color/color_rgba"]; len(raw) != 0 {
		t.Fatal("old spool color returned after reinsertion")
	}
	var reconnected H3C
	fresh, _, _, _ := reconnected.Parse(fixture(t, "h3c-inventory.json"))
	if patchValues(fresh)["/bambu/active_tool"] != `"8"` {
		t.Fatal("reconnect changed home IDs")
	}
	_, _, _, err = parser.Parse([]byte(`{"print":{"command":"push_status","ams":{"tray_exist_bits":"not-hex"}}}`))
	if err == nil {
		t.Fatal("accepted bad tray bits")
	}
	_ = patches
}

func TestH3CFeederRouting(t *testing.T) {
	var parser H3C
	check := func(frame []byte, expected map[string]string) {
		t.Helper()
		patches, _, _, err := parser.Parse(frame)
		if err != nil {
			t.Fatal(err)
		}
		values := patchValues(patches)
		for path, want := range expected {
			if values[path] != want {
				t.Errorf("%s = %q, want %q", path, values[path], want)
			}
		}
	}
	check(fixture(t, "h3c-inventory.json"), map[string]string{
		"/bambu/printtools/0/source": `"ams:32:0"`, "/bambu/printtools/8/source": `"ams:32:8"`,
		"/tools/8.0/material_package_instance/package/material/type": `"PETG"`,
	})
	check([]byte(`{"print":{"command":"push_status","msg":1,"device":{"nozzle":{"info":[{"addr_id":8,"snow":8192}]}}}}`), map[string]string{
		"/bambu/printtools/8/source":                                 `"ams:32:0"`,
		"/tools/8.0/material_package_instance/package/material/type": `"PLA"`,
	})
	check([]byte(`{"print":{"command":"push_status","msg":1,"device":{"nozzle":{"info":[{"addr_id":8,"snow":255}]}}}}`), map[string]string{
		"/bambu/printtools/8/source": "null", "/bambu/printtools/0/source": "",
		"/tools/8.0/enabled": "null",
	})
	check([]byte(`{"print":{"command":"push_status","msg":0}}`), map[string]string{
		"/bambu/printtools/0/source": "null", "/bambu/ams/32/slots/0/present": "null",
	})
}
func TestPackedNozzleWinsOverStaleScalar(t *testing.T) {
	var parser H3C
	_, _, _, _ = parser.Parse(fixture(t, "h3c-running.json"))
	patches, _, _, _ := parser.Parse([]byte(`{"print":{"command":"push_status","nozzle_temper":30,"nozzle_target_temper":0}}`))
	if _, ok := patchValues(patches)["/tools/0/nozzle_temp"]; ok {
		t.Fatal("stale scalar replaced mounted extruder temperature")
	}
	patches, _, _, _ = parser.Parse([]byte(`{"print":{"command":"push_status","nozzle_temper":30,"device":{"extruder":{"info":[{"id":0,"temp":14418125}]}}}}`))
	if patchValues(patches)["/tools/0/nozzle_temp"] != "205" {
		b, _ := json.Marshal(patches)
		t.Fatal(string(b))
	}
}

func TestUnavailableRackReadingClearsExtruderProjection(t *testing.T) {
	var parser H3C
	for i, frame := range []string{
		`{"print":{"command":"push_status","msg":0,"device":{"nozzle":{"info":[{"id":0,"addr_id":8,"temp":4294967295}]},"extruder":{"info":[{"id":0,"temp":14418125}]}}}}`,
		`{"print":{"command":"push_status","msg":1,"device":{"nozzle":{"info":[{"id":0,"addr_id":8,"temp":4294967295}]}}}}`,
	} {
		patches, _, _, err := parser.Parse([]byte(frame))
		if err != nil {
			t.Fatal(err)
		}
		values := patchValues(patches)
		if values["/tools/8/nozzle_temp"] != [...]string{"205", "null"}[i] ||
			values["/tools/8/nozzle_target_temp"] != [...]string{"220", "null"}[i] ||
			values["/bambu/active_nozzle_temp"] != [...]string{"205", "null"}[i] {
			t.Fatalf("frame %d: %v", i, values)
		}
	}
}

func TestNewFullReportClearsUnassignedExtruderReading(t *testing.T) {
	var parser H3C
	first := []byte(`{"print":{"command":"push_status","msg":0,"device":{"nozzle":{"info":[{"id":24,"addr_id":8}]},"extruder":{"info":[{"id":0,"temp":14418125}]}}}}`)
	patches, _, _, err := parser.Parse(first)
	if err != nil || patchValues(patches)["/bambu/active_nozzle_temp"] != "205" {
		t.Fatalf("initial active extruder reading: %v, %v", patchValues(patches), err)
	}
	second := []byte(`{"print":{"command":"push_status","msg":0,"device":{"nozzle":{"info":[{"id":0,"addr_id":8}]}}}}`)
	patches, _, _, err = parser.Parse(second)
	values := patchValues(patches)
	if err != nil || values["/bambu/active_tool"] != `"8"` ||
		values["/bambu/active_nozzle_temp"] != "null" || values["/bambu/active_nozzle_target_temp"] != "null" {
		t.Fatalf("replacement active extruder reading: %v, %v", values, err)
	}
}

func TestFullReportClearsOmittedTopologyAndState(t *testing.T) {
	var parser H3C
	if _, _, _, err := parser.Parse(fixture(t, "h3c-inventory.json")); err != nil {
		t.Fatal(err)
	}
	patches, full, _, err := parser.Parse([]byte(`{"print":{"command":"push_status","msg":0}}`))
	if err != nil || !full {
		t.Fatalf("full report: %v %v", full, err)
	}
	values := patchValues(patches)
	for path, want := range map[string]string{
		"/state": `"UNKNOWN"`, "/bambu/active_tool": "null", "/tools/0/enabled": "false",
		"/bambu/ams/32/slots/0/present": "null", "/bambu/printtools/0/source": "null",
	} {
		if values[path] != want {
			t.Errorf("%s = %s; want %s", path, values[path], want)
		}
	}
}

func TestAmbiguousTrayAndRemovedAMSInvalidateMaterial(t *testing.T) {
	var parser H3C
	if _, _, _, err := parser.Parse(fixture(t, "h3c-inventory.json")); err != nil {
		t.Fatal(err)
	}
	patches, _, _, err := parser.Parse([]byte(`{"print":{"command":"push_status","ams":{"ams":[{"id":"32","tray":[{"id":"0","tray_type":"PLA"},{"id":"0","tray_type":"ABS"}]}]}}}`))
	if err != nil || patchValues(patches)["/tools/0.0/material_package_instance/package/material/type"] != "null" {
		t.Fatalf("ambiguous tray stayed mapped: %v", err)
	}
	patches, _, _, err = parser.Parse([]byte(`{"print":{"command":"push_status","ams":{"ams":[{"id":"1"}]}}}`))
	values := patchValues(patches)
	if err != nil || values["/bambu/ams/32/slots/8/present"] != "null" ||
		values["/tools/8.0/enabled"] != "null" ||
		values["/bambu/printtools/8/source"] != "null" {
		t.Fatalf("missing AMS kept its data: %v", err)
	}
	patches, _, _, err = parser.Parse([]byte(`{"print":{"command":"push_status","device":{"nozzle":{"info":[{"addr_id":8,"snow":8192}]}}}}`))
	if err != nil || patchValues(patches)["/bambu/printtools/8/source"] != "" {
		t.Fatalf("absent AMS accepted a nozzle-only route: %v, %v", patchValues(patches), err)
	}
	patches, _, _, err = parser.Parse([]byte(`{"print":{"command":"push_status","ams":{"ams":[{"id":"32","tray":[{"id":"8","tray_type":"PETG"}]}]}}}`))
	if err != nil || patchValues(patches)["/bambu/printtools/8/source"] != "" {
		t.Fatalf("reattached AMS restored an old route: %v, %v", patchValues(patches), err)
	}
	patches, _, _, err = parser.Parse([]byte(`{"print":{"command":"push_status","device":{"nozzle":{"info":[{"addr_id":8,"snow":8200}]}}}}`))
	if err != nil || patchValues(patches)["/bambu/printtools/8/source"] != `"ams:32:8"` {
		t.Fatalf("fresh nozzle report did not restore the route: %v, %v", patchValues(patches), err)
	}
}
