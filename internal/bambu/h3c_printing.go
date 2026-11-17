package bambu

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path"
	"strconv"
	"strings"

	"github.com/prusa3d/prusa-connect/sdk/connectorapi"
)

// PrintApproval attests to an exact, already-sliced file. PandaLink does not
// validate or slice arbitrary 3MF projects. The manifest is trusted operator input.
type PrintApproval struct {
	Path        string          `json:"path"`
	SHA256      string          `json:"sha256"`
	Size        int64           `json:"size"`
	Model       string          `json:"model"`
	Plate       int             `json:"plate"`
	SinglePlate bool            `json:"single_plate"`
	Filaments   []PrintFilament `json:"filaments"`
}
type PrintFilament struct {
	Tool     string  `json:"tool"`
	Material string  `json:"material"`
	Diameter float64 `json:"nozzle_diameter"`
}

func loadApprovals(file string) (map[string]PrintApproval, error) {
	approvals := make(map[string]PrintApproval)
	if file == "" {
		return approvals, nil
	}
	f, err := os.Open(file)
	if err != nil {
		return nil, InvalidConfig.New("cannot open print_manifest_file")
	}
	defer f.Close()
	if info, err := f.Stat(); err != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return nil, InvalidConfig.New("print manifest must be a regular file of at most 1 MiB")
	}
	var data struct {
		Prints []PrintApproval `json:"prints"`
	}
	d := json.NewDecoder(io.LimitReader(f, 1<<20))
	d.DisallowUnknownFields()
	if d.Decode(&data) != nil || d.Decode(new(any)) != io.EOF || len(data.Prints) > 128 {
		return nil, InvalidConfig.New("invalid print manifest")
	}
	for _, p := range data.Prints {
		_, err := storagePath(p.Path, false)
		digest, e := hex.DecodeString(p.SHA256)
		if err != nil || strings.Count(p.Path, "/") != 1 || e != nil || len(digest) != 32 || p.Size <= 0 || p.Size > maxUploadBytes || p.Model != modelH3C || p.Plate != 1 || !p.SinglePlate || !safeProjectName(path.Base(p.Path)) || len(p.Filaments) < 1 || len(p.Filaments) > 9 {
			return nil, InvalidConfig.New("manifest requires an exact single-plate H3C .gcode.3mf file")
		}
		for _, f := range p.Filaments {
			if !connectorapi.ToolID(f.Tool).Valid() || !strings.HasSuffix(f.Tool, ".0") || len(f.Material) < 1 || len(f.Material) > 32 || f.Diameter <= 0 || f.Diameter > 0.8 {
				return nil, InvalidConfig.New("manifest requires explicit AMS 3H tool, material and nozzle diameter")
			}
		}
		if _, exists := approvals[p.Path]; exists {
			return nil, InvalidConfig.New("duplicate approved print path")
		}
		p.SHA256 = strings.ToLower(p.SHA256)
		approvals[p.Path] = p
	}
	return approvals, nil
}

func safeProjectName(name string) bool {
	stem, ok := strings.CutSuffix(name, ".gcode.3mf")
	return ok && stem != "" && stem[0] != '.' && strings.IndexFunc(stem, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-')
	}) == -1
}

func (s *Session) preparePrint(ctx context.Context, cmd connectorapi.Command) (map[string]any, error) {
	if s.identity.Model != modelH3C {
		return nil, PrintUnsupported.New("this print workflow requires the H3C profile")
	}
	remote, err := storagePath(cmd.Args["path"], false)
	if err != nil {
		return nil, err
	}
	p, ok := s.approvals[cmd.Args["path"]]
	if !ok {
		return nil, PrintUnsupported.New("the exact file must be approved in print_manifest_file")
	}
	tools := make([]string, len(p.Filaments))
	for i, f := range p.Filaments {
		tools[i] = f.Tool
	}
	if raw, ok := cmd.Args["tool_mapping"]; ok {
		if len(raw) > 1024 || json.Unmarshal([]byte(raw), &tools) != nil || len(tools) != len(p.Filaments) {
			return nil, PrintUnsupported.New("tool_mapping must name one SDK feeder tool per slicer filament")
		}
	}
	_, err = s.printMapping(p, tools)
	if err != nil {
		return nil, err
	}
	f, err := s.openStorage(ctx)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	listing, err := listStorage(f, p.Path, remote)
	if err != nil || listing.Directory.Folder || listing.Directory.Size != p.Size {
		return nil, PrintUnsupported.New("approved file is missing or its size changed")
	}
	hash := sha256.New()
	err = f.Retrieve(remote, func(data io.Reader) error {
		n, err := io.Copy(hash, io.LimitReader(data, p.Size+1))
		if err != nil {
			return err
		}
		if n != p.Size {
			return errors.New("printer file size does not match the approval")
		}
		return nil
	})
	if err != nil || hex.EncodeToString(hash.Sum(nil)) != p.SHA256 {
		return nil, PrintUnsupported.New("printer file does not match the approved SHA-256")
	}
	// Require a fresh full report after hashing. Its rack and AMS subtrees
	// replace prior data; a missing block cannot authorize dispatch.
	if _, err := s.refresh(ctx); err != nil {
		return nil, err
	}
	fields, err := s.printMapping(p, tools)
	if err != nil {
		return nil, err
	}
	fields["param"] = "Metadata/plate_1.gcode"
	// Bambu's project_file treats this as a printer-local FTP-root filename,
	// not a URL for PandaLink or the released host to fetch. Root files only.
	fields["url"] = "ftp://" + path.Base(remote)
	fields["file"] = path.Base(remote)
	fields["bed_type"], fields["md5"], fields["extrude_cali_manual_mode"] = "auto", "", 0
	fields["subtask_name"] = strings.TrimSuffix(path.Base(remote), ".gcode.3mf")
	fields["project_id"], fields["profile_id"], fields["task_id"], fields["subtask_id"] = "0", "0", "0", "0"
	// No optional calibration actions. This workflow requires prior calibration.
	for _, key := range []string{"timelapse", "bed_leveling", "flow_cali", "vibration_cali", "layer_inspect"} {
		fields[key] = false
	}
	fields["auto_bed_leveling"], fields["extrude_cali_flag"], fields["nozzle_offset_cali"] = 0, 0, 0
	return fields, nil
}

func (s *Session) printMapping(p PrintApproval, tools []string) (map[string]any, error) {
	s.controlMu.Lock()
	defer s.controlMu.Unlock()
	state := s.values["/state"]
	if state != `"IDLE"` && state != `"FINISHED"` {
		return nil, PrintUnsupported.New("printing is allowed only from known IDLE or FINISHED state")
	}
	flat := make([]int, len(tools))
	detailed := make([]map[string]int, len(tools))
	used := map[string]bool{}
	usedSlots := map[int]bool{}
	for i, t := range tools {
		id := connectorapi.ToolID(t)
		if !id.Valid() || !strings.HasSuffix(t, ".0") || strings.Count(t, ".") != 1 || used[t] {
			return nil, PrintUnsupported.New("ambiguous or unsupported tool mapping")
		}
		used[t] = true
		head := string(id.Head())
		n, err := strconv.Atoi(head)
		if err != nil || n < 0 || n > 8 || head != strconv.Itoa(n) {
			return nil, PrintUnsupported.New("tool is outside the H3C rack")
		}
		if s.values[connectorapi.ToolPath(id.Head(), connectorapi.ToolEnabled)] != "true" {
			return nil, PrintUnsupported.New("mapped PrintTool is not connected")
		}
		typeName := strings.Trim(s.values["/bambu/printtools/"+head+"/type"], `"`)
		if typeName != "HS01" && typeName != "HH01" {
			return nil, PrintUnsupported.New("only reported HS01/HH01 1.75 mm PrintTools are supported")
		}
		switch p.Filaments[i].Material {
		case "PLA", "PETG", "ABS", "ASA", "PVA", "PC", "PA":
		default:
			return nil, PrintUnsupported.New("material is outside the supported standard-filament workflow")
		}
		source := strings.Trim(s.values["/bambu/printtools/"+head+"/source"], `"`)
		if !strings.HasPrefix(source, "ams:32:") {
			return nil, PrintUnsupported.New("mapped tool has no explicit AMS 3H source link")
		}
		slot, err := strconv.Atoi(strings.TrimPrefix(source, "ams:32:"))
		if err != nil || slot < 0 || slot > 8 {
			return nil, PrintUnsupported.New("invalid source link")
		}
		if usedSlots[slot] {
			return nil, PrintUnsupported.New("multiple tools claim the same AMS source")
		}
		usedSlots[slot] = true
		for j := 0; j < 9; j++ {
			if j != n && s.values["/bambu/printtools/"+strconv.Itoa(j)+"/source"] == strconv.Quote(source) {
				return nil, PrintUnsupported.New("another PrintTool claims the mapped AMS source")
			}
		}
		if s.values["/bambu/ams/32/slots/"+strconv.Itoa(slot)+"/present"] != "true" {
			return nil, PrintUnsupported.New("mapped AMS slot is empty or unknown")
		}
		var material string
		_ = json.Unmarshal([]byte(s.values[connectorapi.ToolPath(id, connectorapi.MaterialType)]), &material)
		var diameter float64
		_ = json.Unmarshal([]byte(s.values[connectorapi.ToolPath(id.Head(), connectorapi.FeatureNozzleDiameter)]), &diameter)
		if material != p.Filaments[i].Material || diameter != p.Filaments[i].Diameter {
			return nil, PrintUnsupported.New("mapped material or nozzle diameter is incompatible with the approved slice")
		}
		flat[i] = 28 + slot
		detailed[i] = map[string]int{"ams_id": 32, "slot_id": slot}
	}
	return map[string]any{"use_ams": true, "ams_mapping": flat, "ams_mapping2": detailed, "_inventory_revision": s.inventoryRevision}, nil
}
