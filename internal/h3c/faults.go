package h3c

import (
	"net/http"

	"github.com/prusa3d/prusa-connect/sdk/fault"
)

var faultDomain = func() fault.Domain {
	domain, err := fault.NewDomain("com.github.aux.skaronator.pandalink")
	if err != nil {
		panic(err)
	}
	return domain
}()

// Registered failures never include credentials or raw printer frames.
var (
	PrintUnsupported = fault.Register(faultDomain, "PRINT_UNSUPPORTED", http.StatusConflict, fault.Document(
		"This print cannot be dispatched safely", "Approve the exact H3C slice and supply a compatible, unambiguous AMS 3H mapping.", fault.RetryNever, fault.ShowDetail,
	))
	StorageFailed = fault.Register(faultDomain, "STORAGE_FAILED", http.StatusBadGateway, fault.Document(
		"Printer storage operation failed", "Check local FTPS availability, certificate trust and the destination path.", fault.RetryNever, fault.ShowDetail,
	))
	CommandBusy = fault.Register(faultDomain, "COMMAND_BUSY", http.StatusConflict, fault.Document(
		"Another control is awaiting confirmation", "Wait for that command to finish.", fault.RetryAllowed, fault.ShowDetail,
	))
	CommandFailed = fault.Register(faultDomain, "COMMAND_FAILED", http.StatusGatewayTimeout, fault.Document(
		"Printer acceptance is unknown", "Inspect live state before retrying; cancellation does not undo a physical command.", fault.RetryNever, fault.ShowDetail,
	))
	CommandRefused = fault.Register(faultDomain, "COMMAND_REFUSED", http.StatusConflict, fault.Document(
		"The printer control was refused", "Check printer state and its display.", fault.RetryNever, fault.ShowDetail,
	))
	InvalidConfig = fault.Register(faultDomain, "INVALID_CONFIG", http.StatusBadRequest, fault.Document(
		"PandaLink configuration is invalid", "Check the endpoint address, serial, access-code file and TLS settings.", fault.RetryNever, fault.ShowDetail,
	))
	DeviceUnreachable = fault.Register(faultDomain, "DEVICE_UNREACHABLE", http.StatusBadGateway, fault.Document(
		"The Bambu printer did not answer", "Check connectivity, LAN-only mode, Developer Mode, the access code and the TLS certificate pin.", fault.RetryAllowed, fault.ShowDetail,
	))
	RefreshFailed = fault.Register(faultDomain, "REFRESH_FAILED", http.StatusGatewayTimeout, fault.Document(
		"The printer did not provide a fresh status report", "Check the printer connection and retry the status request.", fault.RetryAllowed, fault.ShowDetail,
	))
)
