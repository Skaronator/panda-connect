// Package pandalink registers the local Bambu Lab connector with Prusa
// Connect. Protocol details live in internal/bambu.
package pandalink

import (
	"time"

	"github.com/prusa3d/prusa-connect/sdk/connectorapi"
	"github.com/prusa3d/prusa-connect/sdk/extension"
	"github.com/skaronator/pandalink/internal/bambu"
)

const (
	Type                  = bambu.Type
	VerbSendStateInfo     = bambu.VerbSendStateInfo
	VerbPausePrint        = bambu.VerbPausePrint
	VerbResumePrint       = bambu.VerbResumePrint
	VerbStopPrint         = bambu.VerbStopPrint
	VerbNozzleTemperature = bambu.VerbNozzleTemperature
	VerbBedTemperature    = bambu.VerbBedTemperature
	VerbStartPrint        = bambu.VerbStartPrint
	DefaultTimeout        = bambu.DefaultTimeout
)

type Driver = bambu.Driver
type Session = bambu.Session

func New(spec connectorapi.Spec) (connectorapi.Driver, error) { return bambu.New(spec) }
func NewDriver(timeout time.Duration) *Driver                 { return bambu.NewDriver(timeout) }
func Doc() extension.Doc                                      { return bambu.Doc() }
