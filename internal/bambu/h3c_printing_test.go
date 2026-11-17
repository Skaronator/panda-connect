package bambu

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/prusa3d/prusa-connect/sdk/connectorapi"
	"github.com/skaronator/pandalink/internal/telemetry"
)

func approveTestPrint(t *testing.T, p *fakePrinter, f *fakeFTPS) PrintApproval {
	t.Helper()
	content := []byte("already sliced and operator-approved H3C data")
	sum := sha256.Sum256(content)
	a := PrintApproval{Path: "sdcard/single.gcode.3mf", SHA256: hex.EncodeToString(sum[:]), Size: int64(len(content)), Model: "H3C", Plate: 1, SinglePlate: true, Filaments: []PrintFilament{{Tool: "0.0", Material: "PLA", Diameter: 0.4}}}
	b, _ := json.Marshal(map[string]any{"prints": []PrintApproval{a}})
	p.endpoint.Options["print_manifest_file"] = writeTestFile(t, string(b))
	f.files["/single.gcode.3mf"] = content
	p.report = fixture(t, "h3c-inventory.json")
	return a
}
func TestApprovedSinglePlateConstructionAndAcceptance(t *testing.T) {
	p := startFake(t)
	f := startFTPS(t, p)
	a := approveTestPrint(t, p, f)
	p.control = func(c *fakeConnection, cmd map[string]json.RawMessage) {
		for key, want := range map[string]string{"command": `"project_file"`, "param": `"Metadata/plate_1.gcode"`, "url": `"ftp://single.gcode.3mf"`, "ams_mapping": "[28]", "ams_mapping2": `[{"ams_id":32,"slot_id":0}]`, "use_ams": "true", "bed_leveling": "false", "auto_bed_leveling": "0", "vibration_cali": "false", "extrude_cali_flag": "0", "nozzle_offset_cali": "0"} {
			if string(cmd[key]) != want {
				t.Errorf("%s=%s want %s", key, cmd[key], want)
			}
		}
		answerControl(p, c, cmd, "success")
	}
	s := connectFake(t, p, time.Second)
	r, err := s.Do(t.Context(), connectorapi.Command{Verb: VerbStartPrint, Args: map[string]string{"path": a.Path}})
	if err != nil || !r.OK {
		t.Fatalf("print: %+v %v", r, err)
	}
}
func TestPrintRefusesUnsafeOrAmbiguousCases(t *testing.T) {
	for _, mode := range []string{"busy", "paused", "unknown file", "checksum", "size", "ambiguous", "mismatch", "empty slot", "missing source", "unsupported tool", "bad mapping", "traversal"} {
		t.Run(mode, func(t *testing.T) {
			p := startFake(t)
			f := startFTPS(t, p)
			a := approveTestPrint(t, p, f)
			s := connectFake(t, p, time.Second)
			args := map[string]string{"path": a.Path}
			s.controlMu.Lock()
			switch mode {
			case "busy":
				s.values["/state"] = `"PRINTING"`
			case "paused":
				s.values["/state"] = `"PAUSED"`
			case "unknown file":
				args["path"] = "sdcard/cache/other.gcode.3mf"
			case "checksum":
				f.mu.Lock()
				f.files["/single.gcode.3mf"] = []byte(strings.Repeat("x", int(a.Size)))
				f.mu.Unlock()
			case "size":
				f.mu.Lock()
				f.files["/single.gcode.3mf"] = []byte("short")
				f.mu.Unlock()
			case "ambiguous":
				a.Filaments = append(a.Filaments, a.Filaments[0])
				s.approvals[a.Path] = a
			case "mismatch":
				s.values["/tools/0/features/nozzle_diameter"] = "0.6"
			case "empty slot":
				s.values["/bambu/ams/32/slots/0/present"] = "false"
			case "missing source":
				delete(s.values, "/bambu/printtools/0/source")
			case "unsupported tool":
				s.values["/bambu/printtools/0/type"] = `"HT01"`
			case "bad mapping":
				args["tool_mapping"] = `["8.0"]`
			case "traversal":
				args["path"] = "sdcard/../single.gcode.3mf"
			}
			s.controlMu.Unlock()
			before := p.requests.Load()
			r, err := s.Do(t.Context(), connectorapi.Command{Verb: VerbStartPrint, Args: args})
			if err == nil || r.OK {
				t.Fatalf("unsafe print accepted: %+v %v", r, err)
			}
			if p.requests.Load() != before {
				t.Fatal("unsafe print reached MQTT")
			}
		})
	}
}
func TestExplicitMultiMaterialMapping(t *testing.T) {
	p := startFake(t)
	s := connectFake(t, p, time.Second)
	var parser telemetry.H3C
	patches, _, _, _ := parser.Parse(fixture(t, "h3c-inventory.json"))
	s.observeControl(patches, time.Now())
	a := PrintApproval{Filaments: []PrintFilament{{Material: "PLA", Diameter: 0.4}, {Material: "PETG", Diameter: 0.4}}}
	fields, err := s.printMapping(a, []string{"0.0", "8.0"})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(fields["ams_mapping"])
	if string(b) != "[28,36]" {
		t.Fatal(string(b))
	}
	if _, err = s.printMapping(a, []string{"0.0", "0.0"}); err == nil {
		t.Fatal("duplicate material source accepted")
	}
}
func TestPrintDisabledWithoutManifest(t *testing.T) {
	s := connectFake(t, startFake(t), time.Second)
	caps, _ := s.Capabilities()
	if caps.Has(VerbStartPrint) {
		t.Fatal("unapproved print advertised")
	}
}
