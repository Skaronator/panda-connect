// Package telemetry translates Bambu status reports into SDK state patches.
package telemetry

import (
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"strings"

	"github.com/prusa3d/prusa-connect/sdk/connectorapi"
)

var errInvalidReport = errors.New("invalid printer status report")

// numeric accepts both JSON numbers and the numeric strings Bambu sends.
type numeric float64

func (n *numeric) UnmarshalJSON(raw []byte) error {
	text := strings.Trim(string(raw), "\"")
	value, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return err
	}
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return errInvalidReport
	}
	*n = numeric(value)
	return nil
}

type printReport struct {
	Command        string          `json:"command"`
	Message        *int            `json:"msg"`
	State          *string         `json:"gcode_state"`
	TaskID         *string         `json:"task_id"`
	SubtaskID      *string         `json:"subtask_id"`
	StartTime      *string         `json:"gcode_start_time"`
	Name           *string         `json:"subtask_name"`
	File           *string         `json:"gcode_file"`
	Progress       *numeric        `json:"mc_percent"`
	Remaining      *numeric        `json:"mc_remaining_time"`
	Layer          *numeric        `json:"layer_num"`
	TotalLayers    *numeric        `json:"total_layer_num"`
	Nozzle         *numeric        `json:"nozzle_temper"`
	NozzleTarget   *numeric        `json:"nozzle_target_temper"`
	NozzleDiameter *numeric        `json:"nozzle_diameter"`
	Bed            *numeric        `json:"bed_temper"`
	BedTarget      *numeric        `json:"bed_target_temper"`
	Chamber        *numeric        `json:"chamber_temper"`
	PrintError     *numeric        `json:"print_error"`
	HMS            json.RawMessage `json:"hms"`
}

type rackPrintReport struct {
	printReport
	Device struct {
		Chamber struct {
			Info struct {
				Temp *numeric `json:"temp"`
			} `json:"info"`
		} `json:"ctc"`
		Bed struct {
			Info struct {
				Temp *uint32 `json:"temp"`
			} `json:"info"`
		} `json:"bed"`
		Extruder struct {
			Info []struct {
				ID   int     `json:"id"`
				Temp *uint32 `json:"temp"`
			} `json:"info"`
		} `json:"extruder"`
	} `json:"device"`
}

// H3C parses status and rack inventory for one H3C connection.
type H3C struct {
	task, subtask, start, name, file, state string
	jobID                                   string
	initialized                             bool
	inventory                               inventory
	packedNozzle                            bool
}

// Parse applies a report and returns patches and its full/status flags.
func (t *H3C) Parse(frame []byte) (patches []connectorapi.StatePatch, full, status bool, err error) {
	var envelope struct {
		Print *rackPrintReport `json:"print"`
	}
	if err = json.Unmarshal(frame, &envelope); err != nil {
		return nil, false, false, err
	}
	p := envelope.Print
	if p == nil || p.Command != "push_status" {
		return nil, false, false, nil
	}
	var hms []json.RawMessage
	if len(p.HMS) != 0 {
		if err := json.Unmarshal(p.HMS, &hms); err != nil {
			return nil, false, false, errInvalidReport
		}
	}
	full = p.Message != nil && *p.Message == 0
	wasKnown := t.inventory.known
	inventoryPatches, inventoryErr := t.inventory.parse(frame, full)
	if inventoryErr != nil {
		return nil, false, false, inventoryErr
	}
	put := func(path string, value any) {
		raw, marshalErr := json.Marshal(value)
		if marshalErr == nil {
			patches = append(patches, connectorapi.StatePatch{Path: path, Value: raw})
		}
	}
	if !t.initialized && !t.inventory.known {
		put(connectorapi.ToolCountPath, 1)
		put("/tools/0/type", connectorapi.ToolTypeHead)
		put("/tools/0/enabled", true)
		put("/tools/0/active", true)
		t.initialized = true
	}
	if !wasKnown && t.inventory.known {
		// Remove the Milestone 1 active-extruder approximation before
		// switching head 0 to its stable PrintTool home-address meaning.
		for _, member := range connectorapi.ToolMembers() {
			put(connectorapi.ToolPath("0", member), nil)
		}
	}
	patches = append(patches, inventoryPatches...)
	for _, field := range []struct {
		incoming *string
		stored   *string
	}{
		{p.TaskID, &t.task}, {p.SubtaskID, &t.subtask}, {p.StartTime, &t.start},
		{p.Name, &t.name}, {p.File, &t.file}, {p.State, &t.state},
	} {
		if field.incoming != nil {
			*field.stored = *field.incoming
		}
	}
	if p.State != nil {
		put("/bambu/gcode_state", t.state)
	} else if full {
		// Do not authorize commands against a prior state when the new
		// snapshot omits it. Retain job identity independently.
		put("/state", "UNKNOWN")
		put("/bambu/gcode_state", nil)
	}
	// Local H3C jobs report task_id even when job_id and subtask_id are "0".
	// Never invent a wall-clock id: reconnecting halfway through would split a job.
	jobID := ""
	if meaningful(t.task) {
		jobID = "task:" + t.task
	}
	if meaningful(t.subtask) {
		jobID = "subtask:" + t.subtask
	}
	if jobID == "" && meaningful(t.start) {
		jobID = "start:" + t.start
	}
	if jobID != "" && t.state != "IDLE" && t.state != "" {
		if jobID != t.jobID {
			put(connectorapi.JobIDPath, jobID)
			for _, field := range []struct {
				path    string
				missing bool
			}{
				{connectorapi.JobOutcomePath, t.state != "FINISH" && t.state != "FAILED"},
				{connectorapi.JobProgressPath, p.Progress == nil},
				{connectorapi.JobTimeRemainingPath, p.Remaining == nil},
				{connectorapi.JobPathPath, p.File == nil},
				{connectorapi.JobDisplayPath, p.Name == nil},
			} {
				if field.missing {
					put(field.path, nil)
				}
			}
			t.jobID = jobID
		}
		if p.Name != nil {
			put(connectorapi.JobDisplayPath, t.name)
		}
		if p.File != nil {
			put(connectorapi.JobPathPath, strings.TrimPrefix(t.file, "/"))
		}
		if p.Progress != nil && *p.Progress >= 0 && *p.Progress <= 100 {
			put(connectorapi.JobProgressPath, int(*p.Progress))
		}
		if p.Remaining != nil && *p.Remaining >= 0 && float64(*p.Remaining) < float64(math.MaxInt64)/60 {
			put(connectorapi.JobTimeRemainingPath, int64(*p.Remaining*60))
		}
		if t.state == "FINISH" {
			put(connectorapi.JobOutcomePath, connectorapi.JobFinished)
		}
		if t.state == "FAILED" {
			put(connectorapi.JobOutcomePath, connectorapi.JobFailed)
		}
		if p.State != nil {
			state := hostState(t.state)
			if t.state == "PREPARE" {
				state = "PRINTING"
			}
			put(connectorapi.JobStatePath, state)
		}
	}
	for _, field := range []struct {
		path  string
		value *numeric
	}{
		{"/tools/0/nozzle_temp", p.Nozzle}, {"/tools/0/nozzle_target_temp", p.NozzleTarget},
		{"/tools/0/features/nozzle_diameter", p.NozzleDiameter},
		{"/temperature/bed", p.Bed}, {"/temperature/bed_target", p.BedTarget},
		{"/temperature/chamber", p.Chamber}, {"/bambu/layer", p.Layer}, {"/bambu/total_layers", p.TotalLayers},
		{"/bambu/print_error", p.PrintError},
	} {
		if field.value != nil {
			if strings.HasPrefix(field.path, "/tools/0/") && (t.inventory.known || t.packedNozzle) {
				continue
			}
			put(field.path, float64(*field.value))
		}
	}
	{
		for _, extruder := range p.Device.Extruder.Info {
			if extruder.ID == 0 && extruder.Temp != nil {
				t.packedNozzle = true
				current, target := *extruder.Temp&0xffff, *extruder.Temp>>16
				if current != 65535 {
					put("/bambu/active_nozzle_temp", current)
				} else {
					put("/bambu/active_nozzle_temp", nil)
				}
				if target != 65535 {
					put("/bambu/active_nozzle_target_temp", target)
				} else {
					put("/bambu/active_nozzle_target_temp", nil)
				}
				head := "0"
				if t.inventory.known {
					head = ""
					if raw, ok := t.inventory.previous["/bambu/active_tool"]; ok {
						_ = json.Unmarshal(raw, &head)
					}
				}
				if head != "" {
					if current != 65535 {
						put(connectorapi.ToolPath(connectorapi.ToolID(head), connectorapi.NozzleTemp), current)
					}
					if target != 65535 {
						put(connectorapi.ToolPath(connectorapi.ToolID(head), connectorapi.NozzleTargetTemp), target)
					}
				}
			}
		}
	}
	if packed := p.Device.Bed.Info.Temp; packed != nil {
		if p.Bed == nil {
			put("/temperature/bed", *packed&0xffff)
		}
		if p.BedTarget == nil {
			put("/temperature/bed_target", *packed>>16)
		}
	}
	if p.Chamber == nil && p.Device.Chamber.Info.Temp != nil {
		put("/temperature/chamber", float64(*p.Device.Chamber.Info.Temp))
	}
	if len(p.HMS) != 0 {
		put("/bambu/hms", hms)
	}
	if p.State != nil {
		put("/state", hostState(t.state))
	}
	return patches, full, true, nil
}

func meaningful(value string) bool { return value != "" && value != "0" }

// The released host's job-history reader still uses the standard state words.
// Keep the vendor's exact word at /bambu/gcode_state, never in that reader's path.
func hostState(state string) string {
	switch state {
	case "IDLE":
		return "IDLE"
	case "PREPARE":
		return "BUSY"
	case "RUNNING":
		return "PRINTING"
	case "PAUSE":
		return "PAUSED"
	case "FINISH":
		return "FINISHED"
	case "FAILED":
		return "ERROR"
	default:
		return "UNKNOWN"
	}
}
