package h3c

import (
	"encoding/json"
	"testing"
)

func TestInventoryAndStableHomeIDs(t *testing.T) {
	var parser telemetry
	patches, _, _, err := parser.parse(fixture(t, "h3c-inventory.json"))
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
	patches, _, _, err = parser.parse([]byte(`{"print":{"command":"push_status","device":{"nozzle":{"exist":33423361,"info":[{"id":24,"addr_id":8},{"id":0,"addr_id":0}]}},"nozzle_temper":30,"nozzle_target_temper":0}}`))
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
	var parser telemetry
	_, _, _, _ = parser.parse(fixture(t, "h3c-inventory.json"))
	patches, _, _, err := parser.parse([]byte(`{"print":{"command":"push_status","ams":{"tray_exist_bits":"1000000000","ams":[{"id":"32","zone_info":[{"zone_id":0,"temp":"60"}]}]},"device":{"nozzle":{"hc":255}}}}`))
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
	patches, _, _, err = parser.parse([]byte(`{"print":{"command":"push_status","ams":{"tray_exist_bits":"1010000000","ams":[{"id":"32","tray":[{"id":"0","tray_type":"ABS"}]}]}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if raw := parser.inventory.previous["/tools/0.0/material_package_instance/package/material/primary_color/color_rgba"]; len(raw) != 0 {
		t.Fatal("old spool color returned after reinsertion")
	}
	var reconnected telemetry
	fresh, _, _, _ := reconnected.parse(fixture(t, "h3c-inventory.json"))
	if patchValues(fresh)["/bambu/active_tool"] != `"8"` {
		t.Fatal("reconnect changed home IDs")
	}
	_, _, _, err = parser.parse([]byte(`{"print":{"command":"push_status","ams":{"tray_exist_bits":"not-hex"}}}`))
	if err == nil {
		t.Fatal("accepted bad tray bits")
	}
	_ = patches
}
func TestPackedNozzleWinsOverStaleScalar(t *testing.T) {
	var parser telemetry
	_, _, _, _ = parser.parse(fixture(t, "h3c-running.json"))
	patches, _, _, _ := parser.parse([]byte(`{"print":{"command":"push_status","nozzle_temper":30,"nozzle_target_temper":0}}`))
	if _, ok := patchValues(patches)["/tools/0/nozzle_temp"]; ok {
		t.Fatal("stale scalar replaced mounted extruder temperature")
	}
	patches, _, _, _ = parser.parse([]byte(`{"print":{"command":"push_status","nozzle_temper":30,"device":{"extruder":{"info":[{"id":0,"temp":14418125}]}}}}`))
	if patchValues(patches)["/tools/0/nozzle_temp"] != "205" {
		b, _ := json.Marshal(patches)
		t.Fatal(string(b))
	}
}

func TestFullReportClearsOmittedTopologyAndState(t *testing.T) {
	var parser telemetry
	if _, _, _, err := parser.parse(fixture(t, "h3c-inventory.json")); err != nil {
		t.Fatal(err)
	}
	patches, full, _, err := parser.parse([]byte(`{"print":{"command":"push_status","msg":0}}`))
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
	var parser telemetry
	if _, _, _, err := parser.parse(fixture(t, "h3c-inventory.json")); err != nil {
		t.Fatal(err)
	}
	patches, _, _, err := parser.parse([]byte(`{"print":{"command":"push_status","ams":{"ams":[{"id":"32","tray":[{"id":"0","tray_type":"PLA"},{"id":"0","tray_type":"ABS"}]}]}}}`))
	if err != nil || patchValues(patches)["/tools/0.0/material_package_instance/package/material/type"] != "null" {
		t.Fatalf("ambiguous tray stayed mapped: %v", err)
	}
	patches, _, _, err = parser.parse([]byte(`{"print":{"command":"push_status","ams":{"ams":[{"id":"1"}]}}}`))
	if err != nil || patchValues(patches)["/bambu/ams/32/slots/8/present"] != "null" {
		t.Fatalf("missing AMS kept its data: %v", err)
	}
}
