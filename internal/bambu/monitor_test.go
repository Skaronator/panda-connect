package bambu

import (
	"encoding/json"
	"testing"
	"testing/synctest"
	"time"

	"github.com/prusa3d/prusa-connect/sdk/connectorapi"
	"github.com/prusa3d/prusa-connect/sdk/connectorapi/connectorapitest"
	"github.com/skaronator/panda-connect/internal/telemetry"
)

func a1Fake(t *testing.T, report string) *fakePrinter {
	t.Helper()
	return startModelFake(t, "A1MINI-TEST-001", "a1-mini-version.json", report)
}

func TestMonitorContract(t *testing.T) {
	connectorapitest.RunDriverContract(t, func(t *testing.T) connectorapitest.Subject {
		p := a1Fake(t, "a1-mini-idle.json")
		return connectorapitest.Subject{Driver: NewDriver(time.Second), Endpoint: p.endpoint, Connectable: true, SafeVerb: VerbSendStateInfo}
	})
}

func TestMonitorAndH3CReconnectIsolation(t *testing.T) {
	h3c := startFake(t)
	a1 := a1Fake(t, "a1-mini-running.synthetic.json")
	// Even H3C-only options and the H3C capability ceiling cannot activate
	// any optional feature or cause the monitor to read a print manifest.
	a1.endpoint.Options["allow_uploads"] = "true"
	a1.endpoint.Options["print_manifest_file"] = "/nonexistent/manifest"
	d := NewDriver(time.Second)
	connect := func(p *fakePrinter) connectorapi.Session {
		t.Helper()
		s, err := d.Connect(t.Context(), p.endpoint, d.Baseline())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
		return s
	}
	h := connect(h3c)
	s := connect(a1)
	id := s.Identity()
	if id.Model != modelA1Mini || id.UnitUID != "bambu:A1MINI-TEST-001" || id.UnitUID == h.Identity().UnitUID {
		t.Fatalf("wrong identity: %+v", id)
	}
	caps, _ := s.Capabilities()
	if len(caps) != 1 || !caps.Has(VerbSendStateInfo) {
		t.Fatalf("monitor capabilities: %+v", caps)
	}
	if _, ok := s.(connectorapi.Filesystem); ok {
		t.Fatal("read-only session exposes a filesystem")
	}
	if _, ok := s.(connectorapi.Transfers); ok {
		t.Fatal("read-only session exposes transfers")
	}
	for _, verb := range []connectorapi.Capability{VerbPausePrint, VerbResumePrint, VerbStopPrint, VerbNozzleTemperature, VerbBedTemperature,
		VerbStartPrint, connectorapi.VerbSendInfo, connectorapi.VerbSendFileInfo, connectorapi.VerbFileUpload} {
		if result, err := s.Do(t.Context(), connectorapi.Command{Verb: verb}); err == nil || result.OK {
			t.Fatalf("monitor accepted %s: %+v, %v", verb, result, err)
		}
	}
	if state := readUntil(t, s.State(), "/state"); string(state["/job/id"]) != `"task:42"` || string(state["/state"]) != `"PRINTING"` {
		t.Fatalf("wrong A1 mini state: %s", state)
	}
	if state := readUntil(t, h.State(), "/state"); string(state["/job/id"]) != `"task:2285"` {
		t.Fatalf("wrong H3C state: %s", state)
	}
	a1.disconnect()
	select {
	case <-s.(monitorSession).Session.(*Session).done:
	case <-time.After(2 * time.Second):
		t.Fatal("A1 mini disconnect did not end its session")
	}
	if result, err := h.Do(t.Context(), connectorapi.Command{Verb: VerbSendStateInfo}); err != nil || !result.OK {
		t.Fatalf("A1 mini disconnect interrupted H3C: %+v, %v", result, err)
	}
	s = connect(a1)
	if state := readUntil(t, s.State(), "/state"); s.Identity() != id || string(state["/job/id"]) != `"task:42"` {
		t.Fatalf("identity changed on reconnect: %+v, %s", s.Identity(), state)
	}
}

func TestRejectsAmbiguousIdentity(t *testing.T) {
	p := a1Fake(t, "a1-mini-idle.json")
	p.version = []byte(`{"info":{"command":"get_version","module":[{"name":"ota","product_name":"Bambu Lab A1 mini","sn":"A1MINI-TEST-001"},{"name":"ota","product_name":"Bambu Lab H3C","sn":"A1MINI-TEST-001"}]}}`)
	d := NewDriver(time.Second)
	if s, err := d.Connect(t.Context(), p.endpoint, d.Baseline()); err == nil || s != nil {
		if s != nil {
			_ = s.Close()
		}
		t.Fatal("accepted the wrong identity")
	}
	if p.requests.Load() != 1 {
		t.Fatal("requested more than identity for a refused printer")
	}
}

func TestCatalogReadOnlyProfiles(t *testing.T) {
	d := NewDriver(time.Second)
	for _, profile := range enabledModels {
		if !profile.monitorOnly() {
			continue
		}
		t.Run(profile.device.Model, func(t *testing.T) {
			p := a1Fake(t, "a1-mini-idle.json")
			version := map[string]any{"info": map[string]any{"command": "get_version", "module": []map[string]string{
				{"name": "ota", "product_name": profile.product, "sn": p.serial, "sw_ver": "01.00.00.00"},
			}}}
			p.version, _ = json.Marshal(version)
			s, err := d.Connect(t.Context(), p.endpoint, d.Baseline())
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if s.Identity().Model != profile.device.Model {
				t.Fatalf("wrong model: %v", s.Identity())
			}
			caps := s.(connectorapi.ReportsProbe).Probe()
			if len(caps) != 1 || !caps.Has(VerbSendStateInfo) {
				t.Fatalf("unsafe capability projection: %v", caps)
			}
			patch := readUntil(t, s.State(), "/state")
			if string(patch["/state"]) != `"IDLE"` || string(patch["/temperature/bed"]) != "26.53125" {
				t.Fatalf("wrong telemetry: %s", patch)
			}
			if !profile.monitor.SingleNozzle && (patch["/tool_count"] != nil || patch["/tools/0/nozzle_temp"] != nil || patch["/bambu/active_nozzle_temp"] == nil) {
				t.Fatalf("inferred a tool assignment: %s", patch)
			}
		})
	}
}

func TestP2SUnmarkedFullAndPackedTemperatures(t *testing.T) {
	p := startModelFake(t, "P2S-TEST-001", "p2s-version.json", "p2s-idle.synthetic.json")
	d := NewDriver(time.Second)
	s, err := d.Connect(t.Context(), p.endpoint, d.Baseline())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.Identity().Model != modelP2S {
		t.Fatalf("wrong model: %v", s.Identity())
	}
	state := readUntil(t, s.State(), "/state")
	if string(state["/temperature/bed"]) != "24" || string(state["/tools/0/nozzle_temp"]) != "25" {
		t.Fatalf("wrong packed temperatures: %s", state)
	}
	if result, err := s.Do(t.Context(), connectorapi.Command{Verb: VerbSendStateInfo}); err != nil || !result.OK {
		t.Fatalf("unmarked snapshot did not satisfy refresh: %v, %v", result, err)
	}
	m, _ := modelForProduct("Bambu Lab P2S")
	parser := telemetry.NewStandard(m.monitor)
	_, _, _, _ = parser.Parse(p.report)
	_, full, status, err := parser.Parse([]byte(`{"print":{"command":"push_status","gcode_state":"RUNNING","mc_percent":25}}`))
	if err != nil || full || status {
		t.Fatal("sparse unmarked update was treated as a full snapshot")
	}
	patches, full, status, err := parser.Parse([]byte(`{"print":{"command":"push_status","msg":1,"device":{"bed_temp":3997756,"extruder":{"info":[{"id":0,"temp":14155775}]}},"bed_temper":60.5}}`))
	values := patchValues(patches)
	if err != nil || full || !status || values["/temperature/bed"] != "60.5" || values["/temperature/bed_target"] != "61" ||
		values["/tools/0/nozzle_temp"] != "null" || values["/tools/0/nozzle_target_temp"] != "215" {
		t.Fatalf("unexpected packed delta: %v, %v", values, err)
	}
}

func TestP2SReconnectDuringPrint(t *testing.T) {
	p := startModelFake(t, "P2S-TEST-001", "p2s-version.json", "p2s-idle.synthetic.json")
	// This is a synthetic status response, not a P2S hardware capture.
	p.report = []byte(`{"print":{"command":"push_status","gcode_state":"RUNNING","task_id":"72","mc_percent":34,"mc_remaining_time":12,"device":{"bed_temp":3932220,"extruder":{"info":[{"id":0,"temp":14418140}]}}}}`)
	d := NewDriver(time.Second)
	var first connectorapi.Identity
	for i := 0; i < 2; i++ {
		s, err := d.Connect(t.Context(), p.endpoint, d.Baseline())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
		state := readUntil(t, s.State(), "/state")
		if string(state["/state"]) != `"PRINTING"` || string(state[connectorapi.JobIDPath]) != `"task:72"` ||
			string(state[connectorapi.JobProgressPath]) != "34" || string(state[connectorapi.JobTimeRemainingPath]) != "720" ||
			state[connectorapi.BuildVolumeXPath] != nil {
			t.Fatalf("wrong mid-print report: %s", state)
		}
		if i == 0 {
			first = s.Identity()
			p.disconnect()
		} else if s.Identity() != first {
			t.Fatal("printer identity changed after reconnect")
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestA1MiniReplay(t *testing.T) {
	reader := telemetry.NewStandard(telemetry.StandardOptions{SingleNozzle: true})
	parse := func(frame []byte, wantFull, wantStatus bool) map[string]string {
		t.Helper()
		patches, full, status, err := reader.Parse(frame)
		if err != nil || full != wantFull || status != wantStatus {
			t.Fatalf("unexpected report classification: full=%v status=%v err=%v", full, status, err)
		}
		return patchValues(patches)
	}
	delta := fixture(t, "a1-mini-paused.synthetic.json")
	parse(delta, false, false)
	idle := parse(fixture(t, "a1-mini-idle.json"), true, true)
	if idle["/state"] != `"IDLE"` || idle["/temperature/bed"] != "26.53125" || idle["/tools/0/nozzle_temp"] != "27.21875" ||
		idle["/temperature/chamber"] != "" || idle["/bambu/inventory_known"] != "" {
		t.Fatalf("idle projection: %v", idle)
	}
	running := parse(fixture(t, "a1-mini-running.synthetic.json"), true, true)
	if running[connectorapi.JobIDPath] != `"task:42"` || running[connectorapi.JobTimeRemainingPath] != "840" {
		t.Fatalf("running projection: %v", running)
	}
	parse([]byte(`{"print":{"msg":1,"result":"success","gcode_state":"IDLE"}}`), false, false)
	paused := parse(delta, false, true)
	if paused["/state"] != `"PAUSED"` || paused[connectorapi.JobProgressPath] != "24" || paused["/bambu/print_error"] != "83935248" ||
		paused["/temperature/bed"] != "" || paused["/bambu/hms"] == "" {
		t.Fatalf("pause delta: %v", paused)
	}
	replacement := parse([]byte(`{"print":{"command":"push_status","msg":0}}`), true, true)
	for _, cleared := range []string{connectorapi.JobIDPath, connectorapi.JobProgressPath, connectorapi.JobTimeRemainingPath, "/tools/0/nozzle_temp", "/temperature/bed", "/bambu/print_error", "/bambu/hms"} {
		if replacement[cleared] != "null" {
			t.Errorf("replacement did not clear %s: %v", cleared, replacement)
		}
	}
	if replacement["/state"] != `"UNKNOWN"` {
		t.Fatalf("replacement invented a state: %v", replacement)
	}
}

func TestMonitorStaleIgnoresNonStatusFrames(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newSession(time.Second, 2*time.Second, "A1MINI-TEST-001", nil, NewDriver(0).log)
		go s.run()
		defer func() { s.stop(); <-s.exited }()
		s.incoming <- receivedFrame{data: fixture(t, "a1-mini-version.json"), at: time.Now()}
		s.incoming <- receivedFrame{data: fixture(t, "a1-mini-idle.json"), at: time.Now()}
		<-s.initialState
		time.Sleep(time.Second)
		s.incoming <- receivedFrame{data: []byte(`{"print":{"result":"success"}}`), at: time.Now()}
		synctest.Wait()
		time.Sleep(time.Second + time.Millisecond)
		synctest.Wait()
		select {
		case <-s.exited:
		default:
			t.Fatal("non-status frame kept the session alive")
		}
	})
}
