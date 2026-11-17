package bambu

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

func TestBackpressureRetainsOneTimeValues(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newSession(time.Second, time.Minute, "H3C-TEST-001", nil, NewDriver(0).log)
		go s.run()
		defer func() { s.stop(); <-s.exited }()
		s.incoming <- receivedFrame{data: fixture(t, "h3c-version.json"), at: time.Now()}
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
