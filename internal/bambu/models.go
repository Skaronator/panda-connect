package bambu

import (
	"github.com/prusa3d/prusa-connect/sdk/connectorapi"
	"github.com/skaronator/panda-connect/internal/telemetry"
)

const (
	modelH3C    = "H3C"
	modelA1Mini = "A1 mini"
	modelP2S    = "P2S"
)

type modelProfile struct {
	product string
	device  connectorapi.DeviceModel
	// Optional features are off unless explicitly reviewed for this model.
	rackInventory, controls, storage, dispatch bool
	monitor                                    telemetry.StandardOptions
	buildVolume                                [3]int
}

// Candidate products must match ota.product_name exactly. Outside H3C, A1
// mini and P2S the spelling comes from the catalog, not a version capture.
// An unmatched candidate needs a capture; never infer identity from a preset,
// an accessory module, or the serial prefix.
var enabledModels = [...]modelProfile{
	{product: "Bambu Lab H3C", device: connectorapi.DeviceModel{Model: modelH3C, DisplayName: "Bambu Lab H3C", Vendor: "Bambu Lab"},
		rackInventory: true, controls: true, storage: true, dispatch: true,
		// Nominal dimensions from https://bambulab.com/en-us/h3c/specs.
		buildVolume: [3]int{330, 320, 325}},
	monitorCandidate(modelA1Mini, telemetry.StandardOptions{SingleNozzle: true}),
	monitorCandidate(modelP2S, telemetry.StandardOptions{SingleNozzle: true, AllowUnmarkedSnapshot: true, PackedTemps: true}),
	monitorCandidate("A1", telemetry.StandardOptions{SingleNozzle: true, AllowUnmarkedSnapshot: true}),
	monitorCandidate("A2L", telemetry.StandardOptions{SingleNozzle: true, AllowUnmarkedSnapshot: true}),
	monitorCandidate("P1P", telemetry.StandardOptions{SingleNozzle: true, AllowUnmarkedSnapshot: true}),
	monitorCandidate("P1S", telemetry.StandardOptions{SingleNozzle: true, AllowUnmarkedSnapshot: true}),
	monitorCandidate("X1", telemetry.StandardOptions{SingleNozzle: true, AllowUnmarkedSnapshot: true}),
	monitorCandidate("X1 Carbon", telemetry.StandardOptions{SingleNozzle: true, AllowUnmarkedSnapshot: true}),
	monitorCandidate("X1E", telemetry.StandardOptions{SingleNozzle: true, AllowUnmarkedSnapshot: true}),
	monitorCandidate("X2D", telemetry.StandardOptions{AllowUnmarkedSnapshot: true, PackedTemps: true}),
	monitorCandidate("H2S", telemetry.StandardOptions{SingleNozzle: true, AllowUnmarkedSnapshot: true}),
	monitorCandidate("H2D", telemetry.StandardOptions{AllowUnmarkedSnapshot: true}),
	monitorCandidate("H2D Pro", telemetry.StandardOptions{AllowUnmarkedSnapshot: true}),
	monitorCandidate("H2C", telemetry.StandardOptions{AllowUnmarkedSnapshot: true}),
}

func monitorCandidate(name string, options telemetry.StandardOptions) modelProfile {
	product := "Bambu Lab " + name
	return modelProfile{product: product, device: connectorapi.DeviceModel{Model: name, DisplayName: product, Vendor: "Bambu Lab"}, monitor: options}
}

func modelForProduct(product string) (modelProfile, bool) {
	for _, model := range enabledModels {
		if model.product == product {
			return model, true
		}
	}
	return modelProfile{}, false
}

func (m modelProfile) monitorOnly() bool { return !m.controls && !m.storage && !m.dispatch }

func supportedDeviceModels() []connectorapi.DeviceModel {
	models := make([]connectorapi.DeviceModel, 0, len(enabledModels))
	for _, model := range enabledModels {
		models = append(models, model.device)
	}
	return models
}
