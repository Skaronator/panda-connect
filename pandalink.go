// Package pandalink registers the local Bambu Lab H3C connector with Prusa
// Connect. Protocol details live in internal/h3c.
package pandalink

import (
	"time"

	"github.com/prusa3d/prusa-connect/sdk/connectorapi"
	"github.com/prusa3d/prusa-connect/sdk/extension"
	"github.com/skaronator/pandalink/internal/h3c"
)

const (
	Type                  = h3c.Type
	VerbSendStateInfo     = h3c.VerbSendStateInfo
	VerbPausePrint        = h3c.VerbPausePrint
	VerbResumePrint       = h3c.VerbResumePrint
	VerbStopPrint         = h3c.VerbStopPrint
	VerbNozzleTemperature = h3c.VerbNozzleTemperature
	VerbBedTemperature    = h3c.VerbBedTemperature
	VerbStartPrint        = h3c.VerbStartPrint
	DefaultTimeout        = h3c.DefaultTimeout
)

type Driver = h3c.Driver
type Session = h3c.Session

func New(spec connectorapi.Spec) (connectorapi.Driver, error) { return h3c.New(spec) }
func NewDriver(timeout time.Duration) *Driver                 { return h3c.NewDriver(timeout) }
func Doc() extension.Doc                                      { return h3c.Doc() }
