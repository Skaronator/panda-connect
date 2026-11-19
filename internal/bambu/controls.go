package bambu

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/prusa3d/prusa-connect/sdk/connectorapi"
	"github.com/skaronator/panda-connect/internal/telemetry"
)

const (
	VerbPausePrint        connectorapi.Capability = "core.command.pause_print"
	VerbResumePrint       connectorapi.Capability = "core.command.resume_print"
	VerbStopPrint         connectorapi.Capability = "core.command.stop_print"
	VerbNozzleTemperature connectorapi.Capability = "core.command.set_nozzle_temperature"
	VerbBedTemperature    connectorapi.Capability = "core.command.set_bed_temperature"
	VerbStartPrint        connectorapi.Capability = "core.command.start_print"
	// Manufacturer's H3C limits, not the legacy P1/X1 limits. See README.md#h3c-controls.
	MaxNozzleTemperature = telemetry.H3CMaxNozzleTemperature
	MaxBedTemperature    = 120
)

type pendingControl struct {
	started                               time.Time
	sequence, command, path, want, before string
	answer                                chan connectorapi.Result
}

type receivedFrame struct {
	data []byte
	at   time.Time
}

func (m modelProfile) capabilities() connectorapi.Set {
	caps := connectorapi.Set{VerbSendStateInfo: {Flag: true}}
	if m.dispatch {
		caps[VerbStartPrint] = connectorapi.Value{Flag: true}
		caps[connectorapi.ArgumentKey(VerbStartPrint, "path")] = connectorapi.Value{Flag: true, Kind: connectorapi.KindString, Required: true, HasLimit: true, Limit: 1024}
		caps[connectorapi.ArgumentKey(VerbStartPrint, "tool_mapping")] = connectorapi.Value{Flag: true, Kind: connectorapi.KindOpaque}
	}
	if m.storage {
		caps[connectorapi.ArgumentKey(connectorapi.VerbSendFileInfo, "path")] = connectorapi.Value{Flag: true, Kind: connectorapi.KindString, Required: true, HasLimit: true, Limit: 1024}
		for _, verb := range []connectorapi.Capability{connectorapi.VerbSendInfo, connectorapi.VerbSendFileInfo, connectorapi.VerbFileUpload} {
			caps[verb] = connectorapi.Value{Flag: true}
		}
	}
	if !m.controls {
		return caps
	}
	for _, verb := range []connectorapi.Capability{VerbPausePrint, VerbResumePrint, VerbStopPrint, VerbNozzleTemperature, VerbBedTemperature} {
		caps[verb] = connectorapi.Value{Flag: true}
	}
	for _, arg := range []struct {
		verb connectorapi.Capability
		name string
		max  float64
	}{
		{VerbNozzleTemperature, "nozzle_temperature", MaxNozzleTemperature},
		{VerbBedTemperature, "bed_temperature", MaxBedTemperature},
	} {
		caps[connectorapi.ArgumentKey(arg.verb, arg.name)] = connectorapi.Value{
			Flag: true, Kind: connectorapi.KindInteger, Required: true, HasMin: true, Min: 0,
			HasLimit: true, Limit: arg.max, Unit: connectorapi.UnitCelsius,
		}
	}
	return caps
}

func (s *Session) control(ctx context.Context, cmd connectorapi.Command) (connectorapi.Result, error) {
	ctx, cancel := s.storageContext(ctx, s.timeout)
	defer cancel()
	select {
	case <-s.done:
		return connectorapi.Result{}, CommandFailed.New("printer disconnected")
	case s.controlLock <- struct{}{}:
		defer func() { <-s.controlLock }()
	default:
		return connectorapi.Result{}, CommandBusy.New("another command is awaiting printer confirmation")
	}
	if err := ctx.Err(); err != nil {
		return connectorapi.Result{}, err
	}
	p := &pendingControl{answer: make(chan connectorapi.Result, 1)}
	fields := map[string]any{}
	var revision uint64
	if cmd.Verb == VerbStartPrint {
		select {
		case s.transferLock <- struct{}{}:
			defer func() { <-s.transferLock }()
		default:
			return connectorapi.Result{}, connectorapi.ErrTransferBusy
		}
		var err error
		fields, err = s.preparePrint(ctx, cmd)
		if err != nil {
			return connectorapi.Result{}, err
		}
		revision, _ = fields["_inventory_revision"].(uint64)
		delete(fields, "_inventory_revision")
	}
	s.controlMu.Lock()
	if err := ctx.Err(); err != nil {
		s.controlMu.Unlock()
		return connectorapi.Result{}, err
	}
	state := s.values["/state"]
	switch cmd.Verb {
	case VerbStartPrint:
		if revision != s.inventoryRevision {
			s.controlMu.Unlock()
			return connectorapi.Result{}, PrintUnsupported.New("inventory changed before print dispatch; inspect it before retrying")
		}
		if state != `"IDLE"` && state != `"FINISHED"` {
			s.controlMu.Unlock()
			return refusedState(), nil
		}
		p.command = "project_file"
	case VerbPausePrint:
		if state != `"PRINTING"` {
			s.controlMu.Unlock()
			return refusedState(), nil
		}
		p.command, p.path, p.want = "pause", "/state", `"PAUSED"`
	case VerbResumePrint:
		if state != `"PAUSED"` {
			s.controlMu.Unlock()
			return refusedState(), nil
		}
		p.command, p.path, p.want = "resume", "/state", `"PRINTING"`
	case VerbStopPrint:
		if state != `"PRINTING"` && state != `"PAUSED"` && state != `"BUSY"` {
			s.controlMu.Unlock()
			return refusedState(), nil
		}
		// A correlated reply confirms stop. A transition to FAILED/ERROR may
		// equally be an unrelated print failure, so does not confirm it.
		p.command = "stop"
	case VerbNozzleTemperature, VerbBedTemperature:
		p.command = "gcode_line"
		if cmd.Verb == VerbNozzleTemperature {
			p.path, p.want = "/bambu/active_nozzle_target_temp", cmd.Args["nozzle_temperature"]
			fields["param"] = "M104 S" + p.want + "\n"
		} else {
			p.path, p.want = "/temperature/bed_target", cmd.Args["bed_temperature"]
			fields["param"] = "M140 S" + p.want + "\n"
		}
	default:
		s.controlMu.Unlock()
		return connectorapi.Result{}, connectorapi.CapabilityNotSupported.New("command is not implemented")
	}
	p.before = s.values[p.path]
	p.sequence = strconv.FormatUint(s.sequence.Add(1), 10)
	fields["command"], fields["sequence_id"] = p.command, p.sequence
	frame, _ := json.Marshal(map[string]any{"print": fields})
	if err := ctx.Err(); err != nil {
		s.controlMu.Unlock()
		return connectorapi.Result{}, err
	}
	p.started = time.Now()
	s.pendingControl = p
	token := s.client.Publish("device/"+s.serial+"/request", 0, false, frame)
	s.controlMu.Unlock()
	defer func() { s.controlMu.Lock(); s.pendingControl = nil; s.controlMu.Unlock() }()
	if err := awaitToken(ctx, token); err != nil {
		return connectorapi.Result{}, CommandFailed.New("command delivery is unconfirmed; inspect printer state before retrying").Wrap(err)
	}
	select {
	case <-ctx.Done():
		return connectorapi.Result{}, CommandFailed.New("printer acceptance is unknown; inspect state before retrying").Wrap(ctx.Err())
	case <-s.done:
		return connectorapi.Result{}, CommandFailed.New("printer disconnected; acceptance is unknown")
	case result := <-p.answer:
		if err := ctx.Err(); err != nil {
			return connectorapi.Result{}, err
		}
		select {
		case <-s.done:
			return connectorapi.Result{}, CommandFailed.New("printer disconnected")
		default:
		}
		return result, nil
	}
}

func refusedState() connectorapi.Result {
	return connectorapi.Result{FaultCode: CommandRefused.Code().String()}
}

// Only exact command + sequence replies may settle an operation. Never expose
// raw replies/reasons: firmware may echo filenames, URLs or credentials.
func (s *Session) controlReply(frame []byte, at time.Time) {
	var envelope struct {
		Print struct {
			Command  string          `json:"command"`
			Sequence json.RawMessage `json:"sequence_id"`
			Result   string          `json:"result"`
		} `json:"print"`
	}
	if json.Unmarshal(frame, &envelope) != nil {
		return
	}
	r := envelope.Print
	s.controlMu.Lock()
	defer s.controlMu.Unlock()
	p := s.pendingControl
	if p == nil || at.Before(p.started) || r.Command != p.command || strings.Trim(string(r.Sequence), `"`) != p.sequence {
		return
	}
	var answer connectorapi.Result
	switch strings.ToLower(r.Result) {
	case "success":
		answer.OK = true
	case "fail", "failed", "failure":
		answer.FaultCode = CommandRefused.Code().String()
	default:
		return
	}
	select {
	case p.answer <- answer:
	default:
	}
}

func (s *Session) observeControl(patches []connectorapi.StatePatch, at time.Time) {
	s.controlMu.Lock()
	defer s.controlMu.Unlock()
	for _, patch := range patches {
		// Keep only command-relevant values, not the complete untrusted tree.
		if patch.Path == "/state" || strings.HasPrefix(patch.Path, "/bambu/") || strings.HasPrefix(patch.Path, "/tools/") || patch.Path == "/temperature/bed_target" {
			if strings.HasPrefix(patch.Path, "/bambu/ams/") || strings.HasPrefix(patch.Path, "/bambu/printtools/") || strings.HasPrefix(patch.Path, "/tools/") || patch.Path == "/state" {
				if s.values[patch.Path] != string(patch.Value) {
					s.inventoryRevision++
				}
			}
			if string(patch.Value) == "null" {
				delete(s.values, patch.Path)
			} else {
				s.values[patch.Path] = string(patch.Value)
			}
		}
		p := s.pendingControl
		if p != nil && !at.Before(p.started) && p.path != "" && patch.Path == p.path && string(patch.Value) == p.want && p.before != p.want {
			select {
			case p.answer <- connectorapi.Result{OK: true}:
			default:
			}
		}
	}
}
