package telemetry

import (
	"encoding/json"
	"math"
	"strings"

	"github.com/prusa3d/prusa-connect/sdk/connectorapi"
)

// StandardOptions selects the status shapes understood for a model.
type StandardOptions struct {
	SingleNozzle          bool
	AllowUnmarkedSnapshot bool
	PackedTemps           bool
}

// Standard projects the shared read-only fields. Only single-nozzle
// profiles assign readings to a tool index; no feeder topology is inferred.
type Standard struct {
	config                          StandardOptions
	initialized                     bool
	state, task, subtask, start, id string
}

// NewStandard creates a parser for one printer connection.
func NewStandard(config StandardOptions) *Standard { return &Standard{config: config} }

// Parse applies a report and returns patches and its full/status flags.
func (t *Standard) Parse(frame []byte) (patches []connectorapi.StatePatch, full, status bool, err error) {
	// Classify first so an unrelated reply cannot be mistaken for a delta or
	// cause type errors while decoding fields that are only used in status.
	var envelope struct {
		Print json.RawMessage `json:"print"`
	}
	if err = json.Unmarshal(frame, &envelope); err != nil || len(envelope.Print) == 0 {
		return nil, false, false, err
	}
	var header struct {
		Command string          `json:"command"`
		Message *int            `json:"msg"`
		Result  json.RawMessage `json:"result"`
	}
	if err = json.Unmarshal(envelope.Print, &header); err != nil {
		return nil, false, false, err
	}
	if len(header.Result) != 0 {
		return nil, false, false, nil
	}
	full = header.Command == "push_status" && header.Message != nil && *header.Message == 0
	delta := (header.Command == "push_status" || header.Command == "") && header.Message != nil && *header.Message == 1
	unmarked := t.config.AllowUnmarkedSnapshot && header.Command == "push_status" && header.Message == nil
	if !full && !unmarked && (!delta || !t.initialized) {
		return nil, false, false, nil
	}
	var p struct {
		printReport
		Device json.RawMessage `json:"device"`
	}
	if err = json.Unmarshal(envelope.Print, &p); err != nil {
		return nil, false, false, err
	}
	var packedBed, packedNozzle *uint32
	if t.config.PackedTemps && len(p.Device) > 0 {
		var device struct {
			BedTemp *uint32 `json:"bed_temp"`
			Bed     struct {
				Info struct {
					Temp *uint32 `json:"temp"`
				} `json:"info"`
			} `json:"bed"`
			Extruder struct {
				Info []struct {
					ID   *int    `json:"id"`
					Temp *uint32 `json:"temp"`
				} `json:"info"`
			} `json:"extruder"`
		}
		if err = json.Unmarshal(p.Device, &device); err != nil {
			return nil, false, false, err
		}
		packedBed = device.Bed.Info.Temp
		if packedBed == nil {
			packedBed = device.BedTemp
		}
		if ex := device.Extruder.Info; t.config.SingleNozzle && len(ex) == 1 && ex[0].ID != nil && *ex[0].ID == 0 {
			packedNozzle = ex[0].Temp
		}
	}
	if unmarked {
		// Some firmware, including P2S, omits msg on pushall. A sparse
		// unmarked update cannot satisfy refresh or replace a snapshot.
		full = p.State != nil && *p.State != "" && (p.Bed != nil || packedBed != nil) && (p.Nozzle != nil || packedNozzle != nil)
		if !full {
			return nil, false, false, nil
		}
	}
	applyPacked(packedBed, &p.Bed, &p.BedTarget)
	applyPacked(packedNozzle, &p.Nozzle, &p.NozzleTarget)
	var hms []json.RawMessage
	if len(p.HMS) != 0 && json.Unmarshal(p.HMS, &hms) != nil {
		return nil, false, false, errInvalidReport
	}
	put := func(path string, value any) {
		raw, _ := json.Marshal(value) // only JSON-safe scalars and decoded HMS values
		patches = append(patches, connectorapi.StatePatch{Path: path, Value: raw})
	}
	if !t.initialized && t.config.SingleNozzle {
		put(connectorapi.ToolCountPath, 1)
		put("/tools/0/type", connectorapi.ToolTypeHead)
		put("/tools/0/enabled", true)
		put("/tools/0/active", true)
	}
	t.initialized = true
	nozzlePath, targetPath := "/bambu/active_nozzle_temp", "/bambu/active_nozzle_target_temp"
	if t.config.SingleNozzle {
		nozzlePath, targetPath = "/tools/0/nozzle_temp", "/tools/0/nozzle_target_temp"
	}
	for _, field := range []struct {
		path  string
		value *numeric
		clear bool
	}{
		{nozzlePath, p.Nozzle, packedNozzle != nil}, {targetPath, p.NozzleTarget, packedNozzle != nil},
		{"/temperature/bed", p.Bed, packedBed != nil}, {"/temperature/bed_target", p.BedTarget, packedBed != nil},
		{"/bambu/layer", p.Layer, false}, {"/bambu/total_layers", p.TotalLayers, false},
		{"/bambu/print_error", p.PrintError, false},
	} {
		if field.value != nil {
			put(field.path, *field.value)
		} else if full || field.clear {
			put(field.path, nil)
		}
	}
	if t.config.SingleNozzle {
		if p.NozzleDiameter != nil {
			put("/tools/0/features/nozzle_diameter", *p.NozzleDiameter)
		} else if full {
			put("/tools/0/features/nozzle_diameter", nil)
		}
	}
	if full || len(p.HMS) != 0 {
		put("/bambu/hms", hms)
	}
	oldID := t.id
	if full {
		t.state, t.task, t.subtask, t.start = "", "", "", ""
	} else if (p.TaskID != nil && *p.TaskID != t.task) || (p.SubtaskID != nil && *p.SubtaskID != t.subtask) ||
		(p.StartTime != nil && meaningful(t.start) && *p.StartTime != t.start) {
		// Avoid combining identifiers from two different jobs. Keep parent
		// identity when only a subtask is supplied, but not the prior start.
		t.start = ""
		if p.TaskID != nil && *p.TaskID != t.task {
			t.subtask = ""
		}
	}
	for _, field := range []struct {
		src *string
		dst *string
	}{{p.State, &t.state}, {p.TaskID, &t.task}, {p.SubtaskID, &t.subtask}, {p.StartTime, &t.start}} {
		if field.src != nil {
			*field.dst = *field.src
		}
	}
	if p.State != nil || full {
		if t.state == "" {
			put("/bambu/gcode_state", nil)
		} else {
			put("/bambu/gcode_state", t.state)
		}
		put("/state", hostState(t.state))
	}
	t.id = ""
	if t.state == "IDLE" {
		t.task, t.subtask, t.start = "", "", ""
	} else if t.state != "" {
		switch {
		case meaningful(t.subtask):
			t.id = "subtask:" + t.subtask
		case meaningful(t.task):
			t.id = "task:" + t.task
		case meaningful(t.start):
			t.id = "start:" + t.start
		}
	}
	replace := full || oldID != t.id
	if t.id == "" {
		if replace {
			for _, path := range []string{connectorapi.JobIDPath, connectorapi.JobDisplayPath, connectorapi.JobPathPath,
				connectorapi.JobProgressPath, connectorapi.JobTimeRemainingPath, connectorapi.JobOutcomePath, connectorapi.JobStatePath} {
				put(path, nil)
			}
		}
		return patches, full, true, nil
	}
	if replace {
		put(connectorapi.JobIDPath, t.id)
	}
	if p.Name != nil && *p.Name != "" {
		put(connectorapi.JobDisplayPath, *p.Name)
	} else if replace || p.Name != nil {
		put(connectorapi.JobDisplayPath, nil)
	}
	if p.File != nil && strings.TrimPrefix(*p.File, "/") != "" {
		put(connectorapi.JobPathPath, strings.TrimPrefix(*p.File, "/"))
	} else if replace || p.File != nil {
		put(connectorapi.JobPathPath, nil)
	}
	if p.Progress != nil && *p.Progress >= 0 && *p.Progress <= 100 {
		put(connectorapi.JobProgressPath, int(*p.Progress))
	} else if replace || p.Progress != nil {
		put(connectorapi.JobProgressPath, nil)
	}
	if p.Remaining != nil && *p.Remaining >= 0 && float64(*p.Remaining) < float64(math.MaxInt64)/60 {
		put(connectorapi.JobTimeRemainingPath, int64(*p.Remaining*60))
	} else if replace || p.Remaining != nil {
		put(connectorapi.JobTimeRemainingPath, nil)
	}
	if replace || p.State != nil {
		switch t.state {
		case "FINISH":
			put(connectorapi.JobOutcomePath, connectorapi.JobFinished)
		case "FAILED":
			put(connectorapi.JobOutcomePath, connectorapi.JobFailed)
		default:
			put(connectorapi.JobOutcomePath, nil)
		}
		state := hostState(t.state)
		if t.state == "PREPARE" {
			state = "PRINTING"
		}
		put(connectorapi.JobStatePath, state)
	}
	return patches, full, true, nil
}

func applyPacked(word *uint32, current, target **numeric) {
	if word == nil {
		return
	}
	if *current == nil && *word&0xffff != 0xffff {
		value := numeric(*word & 0xffff)
		*current = &value
	}
	if *target == nil && *word>>16 != 0xffff {
		value := numeric(*word >> 16)
		*target = &value
	}
}
