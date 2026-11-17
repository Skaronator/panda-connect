package h3c

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/prusa3d/prusa-connect/sdk/connectorapi"
)

// Home addresses are logical tool identities. The wire id changes when a
// PrintTool moves from its rack to the one extruder; it is not a stable ID.
type nozzleInfo struct {
	ID       *int     `json:"id"`
	Address  *int     `json:"addr_id"`
	Source   *int     `json:"snow"`
	Type     *string  `json:"type"`
	Diameter *numeric `json:"diameter"`
	Temp     *uint32  `json:"temp"`
	Maximum  *numeric `json:"tm"`
}
type trayInfo struct {
	ID       string   `json:"id"`
	Material *string  `json:"tray_type"`
	Color    *string  `json:"tray_color"`
	Min      *numeric `json:"nozzle_temp_min"`
	Max      *numeric `json:"nozzle_temp_max"`
	Remain   *numeric `json:"remain"`
}
type zoneInfo struct {
	ID       int      `json:"zone_id"`
	Slots    []int    `json:"zone_slots"`
	Humidity *numeric `json:"humidity_raw"`
	Index    *numeric `json:"humidity"`
	Temp     *numeric `json:"temp"`
	Time     *numeric `json:"dry_time"`
	DryInfo  *numeric `json:"zone_dry_info"`
}
type inventory struct {
	known     bool
	hc, exist *uint32
	nozzles   map[int]nozzleInfo
	trays     map[int]trayInfo
	zones     map[int]zoneInfo
	bits      *uint64
	previous  map[string]json.RawMessage
}

func (v *inventory) parse(frame []byte, full bool) ([]connectorapi.StatePatch, error) {
	var e struct {
		Print struct {
			Device struct {
				Nozzle *struct {
					HC    *uint32      `json:"hc"`
					Exist *uint32      `json:"exist"`
					Info  []nozzleInfo `json:"info"`
				} `json:"nozzle"`
			} `json:"device"`
			AMS *struct {
				Bits  *string `json:"tray_exist_bits"`
				Units []struct {
					ID    string     `json:"id"`
					Trays []trayInfo `json:"tray"`
					Zones []zoneInfo `json:"zone_info"`
				} `json:"ams"`
			} `json:"ams"`
		} `json:"print"`
	}
	if err := json.Unmarshal(frame, &e); err != nil {
		return nil, err
	}
	p := e.Print
	// Validate before applying any part of this delta.
	var bits *uint64
	if a := p.AMS; a != nil && a.Bits != nil {
		value, err := strconv.ParseUint(*a.Bits, 16, 64)
		if err != nil {
			return nil, errInvalidReport
		}
		bits = &value
	}
	if n := p.Device.Nozzle; n != nil {
		seen := [9]bool{}
		for _, item := range n.Info {
			if item.Address == nil || *item.Address < 0 || *item.Address > 8 {
				continue
			}
			i := *item.Address
			if seen[i] || (item.ID != nil && *item.ID != 0 && *item.ID != 16+i) {
				return nil, errInvalidReport
			}
			seen[i] = true
		}
	}
	if v.nozzles == nil {
		v.nozzles = make(map[int]nozzleInfo)
		v.trays = make(map[int]trayInfo)
		v.zones = make(map[int]zoneInfo)
	}
	if full {
		clear(v.nozzles)
		clear(v.trays)
		clear(v.zones)
		v.hc, v.exist, v.bits = nil, nil, nil
	}
	if n := p.Device.Nozzle; n != nil {
		v.known = true
		if n.Info != nil && len(n.Info) == 0 {
			clear(v.nozzles)
		}
		if n.HC != nil {
			v.hc = n.HC
		}
		if n.Exist != nil {
			v.exist = n.Exist
		}
		for _, n := range n.Info {
			if n.Address == nil || *n.Address < 0 || *n.Address > 8 {
				continue
			}
			old := v.nozzles[*n.Address]
			if n.ID != nil {
				old.ID = n.ID
			}
			old.Address = n.Address
			if n.Source != nil {
				old.Source = n.Source
			}
			if n.Type != nil {
				old.Type = n.Type
			}
			if n.Diameter != nil {
				old.Diameter = n.Diameter
			}
			if n.Temp != nil {
				old.Temp = n.Temp
			}
			if n.Maximum != nil {
				old.Maximum = n.Maximum
			}
			v.nozzles[*n.Address] = old
		}
	}
	if a := p.AMS; a != nil {
		racks := 0
		for _, u := range a.Units {
			if u.ID == "32" {
				racks++
			}
		}
		if a.Units != nil && racks != 1 {
			v.trays = make(map[int]trayInfo)
			v.zones = make(map[int]zoneInfo)
			v.bits = nil
			bits = nil
		}
		if bits != nil {
			v.bits = bits
		}
		for _, u := range a.Units {
			if u.ID != "32" || racks != 1 {
				continue
			}
			if u.Trays != nil && len(u.Trays) == 0 {
				clear(v.trays)
			}
			if u.Zones != nil && len(u.Zones) == 0 {
				clear(v.zones)
			}
			seen, ambiguous := [9]bool{}, [9]bool{}
			for _, t := range u.Trays {
				id, err := strconv.Atoi(t.ID)
				if err != nil || id < 0 || id > 8 {
					continue
				}
				if seen[id] {
					delete(v.trays, id)
					ambiguous[id] = true
				}
				seen[id] = true
				if ambiguous[id] {
					continue
				}
				old := v.trays[id]
				if t.Material != nil {
					if old.Material == nil || *t.Material != *old.Material {
						old = trayInfo{}
					}
					old.Material = t.Material
				}
				if t.Color != nil {
					old.Color = t.Color
				}
				if t.Min != nil {
					old.Min = t.Min
				}
				if t.Max != nil {
					old.Max = t.Max
				}
				if t.Remain != nil {
					old.Remain = t.Remain
				}
				v.trays[id] = old
			}
			for _, z := range u.Zones {
				if z.ID < 0 || z.ID > 8 {
					continue
				}
				old := v.zones[z.ID]
				old.ID = z.ID
				if z.Slots != nil {
					old.Slots = z.Slots
				}
				if z.Humidity != nil {
					old.Humidity = z.Humidity
				}
				if z.Index != nil {
					old.Index = z.Index
				}
				if z.Temp != nil {
					old.Temp = z.Temp
				}
				if z.Time != nil {
					old.Time = z.Time
				}
				if z.DryInfo != nil {
					old.DryInfo = z.DryInfo
				}
				v.zones[z.ID] = old
			}
		}
	}
	if v.bits != nil {
		for i := range v.trays {
			if *v.bits&(1<<uint(28+i)) == 0 {
				delete(v.trays, i)
			}
		}
	}
	if !v.known {
		return nil, nil
	}
	next := make(map[string]json.RawMessage)
	put := func(p string, x any) {
		b, err := json.Marshal(x)
		if err == nil {
			next[p] = b
		}
	}
	put(connectorapi.ToolCountPath, 9)
	put("/bambu/inventory_known", true)
	active := -1
	for i := 0; i < 9; i++ {
		n, found := v.nozzles[i]
		head := connectorapi.HeadID(i)
		base := "/bambu/printtools/" + string(head)
		if v.hc != nil {
			put(base+"/connected", *v.hc&(1<<i) != 0)
		}
		enabled := found
		if v.hc != nil {
			enabled = enabled && *v.hc&(1<<i) != 0
		}
		if v.exist != nil && n.ID != nil {
			enabled = enabled && *v.exist&(1<<*n.ID) != 0
			put(base+"/installed", enabled)
		}
		put(connectorapi.ToolPath(head, connectorapi.ToolEnabled), enabled)
		if !enabled {
			delete(v.nozzles, i) // A reconnected tool must report its properties anew.
			continue
		}
		put(connectorapi.ToolPath(head, connectorapi.ToolType), connectorapi.ToolTypeHead)
		if n.ID != nil {
			mounted := *n.ID == 0
			put(base+"/mounted", mounted)
			put(connectorapi.ToolPath(head, connectorapi.ToolActive), mounted)
			if mounted {
				if active == -1 {
					active = i
				} else {
					active = -2
				}
			}
		}
		if n.Diameter != nil && *n.Diameter > 0 && *n.Diameter <= 2 {
			put(connectorapi.ToolPath(head, connectorapi.FeatureNozzleDiameter), n.Diameter)
		}
		if n.Type != nil {
			t := *n.Type
			if len(t) == 4 && t[0] == 'H' {
				put(base+"/type", t)
				switch t[1] {
				case 'S':
					put(connectorapi.ToolPath(head, connectorapi.FeatureNozzleHighFlow), false)
				case 'H':
					put(connectorapi.ToolPath(head, connectorapi.FeatureNozzleHighFlow), true)
				}
				switch t[2:] {
				case "00":
					put(connectorapi.ToolPath(head, connectorapi.FeatureNozzleHardened), false)
				case "01", "05":
					put(connectorapi.ToolPath(head, connectorapi.FeatureNozzleHardened), true)
				}
			}
		}
		if n.Maximum != nil && *n.Maximum > 0 && *n.Maximum <= MaxNozzleTemperature {
			put(base+"/max_temperature", n.Maximum)
		}
		if n.Temp != nil {
			if current := *n.Temp & 0xffff; current != 65535 {
				put(connectorapi.ToolPath(head, connectorapi.NozzleTemp), current)
			}
			if target := *n.Temp >> 16; target != 65535 {
				put(connectorapi.ToolPath(head, connectorapi.NozzleTargetTemp), target)
			}
		}
		if n.Source != nil {
			source := *n.Source
			unit, slot := source>>8, source&255
			if unit == 32 && slot < 9 {
				put(base+"/source", fmt.Sprintf("ams:32:%d", slot))
				put(connectorapi.ToolPath(head, connectorapi.FeederSlotCount), 1)
				put(connectorapi.ToolPath(head, connectorapi.FeederModel), "AMS 3H")
				child := head.Slot(0)
				put(connectorapi.ToolPath(child, connectorapi.ToolEnabled), true)
				put(connectorapi.ToolPath(child, connectorapi.ToolType), connectorapi.ToolTypeFeederSlot)
				if n.ID != nil {
					put(connectorapi.ToolPath(child, connectorapi.ToolActive), *n.ID == 0)
				}
				v.putMaterial(slot, func(member string, x any) { put(connectorapi.ToolPath(child, member), x) })
			}
		}
	}
	if active >= 0 {
		put("/bambu/active_tool", strconv.Itoa(active))
	}
	for i := 0; i < 9; i++ {
		base := fmt.Sprintf("/bambu/ams/32/slots/%d", i)
		if v.bits != nil {
			exists := *v.bits&(1<<uint(28+i)) != 0
			put(base+"/present", exists)
		}
		v.putMaterial(i, func(member string, x any) { put(base+"/"+member, x) })
	}
	for id, z := range v.zones {
		base := fmt.Sprintf("/bambu/ams/32/zones/%d", id)
		if z.Slots != nil {
			valid := true
			for _, s := range z.Slots {
				if s < 0 || s > 8 {
					valid = false
				}
			}
			if valid {
				put(base+"/slots", z.Slots)
			}
		}
		for _, f := range []struct {
			name string
			n    *numeric
			max  numeric
		}{{"humidity", z.Humidity, 100}, {"humidity_index", z.Index, 5}, {"temperature", z.Temp, 120}, {"dry_time", z.Time, 100000}, {"dry_info", z.DryInfo, 65535}} {
			if f.n != nil && *f.n >= 0 && *f.n <= f.max {
				put(base+"/"+f.name, f.n)
			}
		}
	}
	var patches []connectorapi.StatePatch
	for p := range v.previous {
		if _, ok := next[p]; !ok {
			patches = append(patches, connectorapi.StatePatch{Path: p, Value: json.RawMessage("null")})
		}
	}
	for p, b := range next {
		if string(v.previous[p]) != string(b) {
			patches = append(patches, connectorapi.StatePatch{Path: p, Value: b})
		}
	}
	sort.Slice(patches, func(i, j int) bool { return patches[i].Path < patches[j].Path })
	v.previous = next
	return patches, nil
}

func (v *inventory) putMaterial(slot int, put func(string, any)) {
	if v.bits != nil && *v.bits&(1<<uint(28+slot)) == 0 {
		put(connectorapi.MaterialAbbreviation, connectorapi.NoMaterial)
		return
	}
	t, ok := v.trays[slot]
	if !ok {
		return
	}
	if t.Material != nil {
		if *t.Material == "" {
			return
		}
		put(connectorapi.MaterialAbbreviation, *t.Material)
		put(connectorapi.MaterialType, *t.Material)
	}
	if t.Color != nil {
		c := "#" + strings.TrimPrefix(*t.Color, "#")
		if connectorapi.ValidColor(c) {
			put(connectorapi.MaterialColor, c)
		}
	}
	if t.Min != nil && *t.Min >= 0 && *t.Min <= 350 {
		put(connectorapi.MaterialProperty("min_print_temperature"), t.Min)
	}
	if t.Max != nil && *t.Max >= 0 && *t.Max <= 350 {
		put(connectorapi.MaterialProperty("max_print_temperature"), t.Max)
	}
}
