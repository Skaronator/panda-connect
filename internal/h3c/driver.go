// Package pandalink connects Bambu Lab H3C printers to Prusa Connect over local MQTT.
package h3c

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/prusa3d/prusa-connect/sdk/connectorapi"
	"github.com/prusa3d/prusa-connect/sdk/extension"
	"github.com/skaronator/pandalink/internal/printertls"
	"github.com/skaronator/pandalink/internal/secret"
)

const (
	// Type selects PandaLink in core.connectors.
	Type connectorapi.TypeName = "pandalink"
	// VerbSendStateInfo requests a full telemetry report, without changing the printer.
	VerbSendStateInfo connectorapi.Capability = "core.command.send_state_info"
	// DefaultTimeout bounds the MQTT handshake and an explicit refresh.
	DefaultTimeout = 15 * time.Second
	maxFrameBytes  = 256 << 10
)

func init() { connectorapi.Register(Type, Doc(), New, dialect()) }

// Doc describes the driver and its connector-wide settings.
func Doc() extension.Doc {
	return extension.Doc{
		Summary: "Local H3C monitoring, confirmed controls, FTPS and approved single-plate printing.",
		Description: "Each endpoint supplies serial, access_code_file and tls_fingerprint (or ca_file). " +
			"LAN-only and Developer Mode are required. Controls wait for printer confirmation. " +
			"Print dispatch requires an explicit print_manifest_file. Rack/AMS inventory is read-only.",
		Version: "0.2.0", Contract: connectorapi.Contract, Stability: extension.StabilityExperimental,
		Options: []extension.Option{
			{Key: "timeout", Type: extension.OptionDuration, Default: DefaultTimeout.String(), Summary: "Maximum time for a connection, command, listing or print preflight; uploads have a 10-minute ceiling."},
			{Key: "stale_after", Type: extension.OptionDuration, Default: "2m0s", Summary: "End the session if no printer telemetry arrives within this time."},
		},
	}
}

func dialect() connectorapi.Dialect {
	names := []string{"IDLE", "BUSY", "PRINTING", "PAUSED", "FINISHED", "ERROR", "UNKNOWN"}
	set := connectorapi.StateSet{}
	for _, name := range names {
		accepts := []connectorapi.Capability{VerbSendStateInfo, connectorapi.VerbSendInfo, connectorapi.VerbSendFileInfo, VerbNozzleTemperature, VerbBedTemperature}
		switch name {
		case "IDLE", "FINISHED":
			accepts = append(accepts, VerbStartPrint)
		case "PRINTING":
			accepts = append(accepts, VerbPausePrint, VerbStopPrint)
		case "PAUSED":
			accepts = append(accepts, VerbResumePrint, VerbStopPrint)
		case "BUSY":
			accepts = append(accepts, VerbStopPrint)
		}
		set.States = append(set.States, connectorapi.DeviceState{Name: connectorapi.StateName(name), DisplayName: name, Accepts: accepts})
		// Firmware may enter any of these states without a host command.
		for _, next := range names {
			if name != next {
				set.Transitions = append(set.Transitions, connectorapi.Transition{From: connectorapi.StateName(name), To: connectorapi.StateName(next)})
			}
		}
	}
	set.Transitions = append(set.Transitions,
		connectorapi.Transition{From: "PRINTING", To: "PAUSED", Verb: VerbPausePrint},
		connectorapi.Transition{From: "PAUSED", To: "PRINTING", Verb: VerbResumePrint},
		connectorapi.Transition{From: "IDLE", To: "BUSY", Verb: VerbStartPrint},
		connectorapi.Transition{From: "FINISHED", To: "BUSY", Verb: VerbStartPrint},
	)
	for _, from := range []connectorapi.StateName{"PRINTING", "PAUSED", "BUSY"} {
		set.Transitions = append(set.Transitions, connectorapi.Transition{From: from, To: "ERROR", Verb: VerbStopPrint})
	}
	return connectorapi.Dialect{
		Brands: []string{"Bambu Lab"}, Needs: connectorapi.NeedsAddress,
		Models: []connectorapi.DeviceModel{{Model: "H3C", DisplayName: "Bambu Lab H3C", Vendor: "Bambu Lab"}}, States: set,
	}
}

// Driver holds connector-wide settings, never printer state.
type Driver struct {
	timeout    time.Duration
	staleAfter time.Duration
	log        *slog.Logger
}

var _ connectorapi.Outbound = (*Driver)(nil)

// New constructs the registered driver without contacting a printer.
func New(spec connectorapi.Spec) (connectorapi.Driver, error) {
	cfg := struct {
		Timeout    string `json:"timeout"`
		StaleAfter string `json:"stale_after"`
	}{}
	if err := spec.DecodeConfig(&cfg); err != nil {
		return nil, InvalidConfig.New("cannot decode connector settings")
	}
	d := NewDriver(0)
	for _, setting := range []struct {
		value  string
		target *time.Duration
	}{{cfg.Timeout, &d.timeout}, {cfg.StaleAfter, &d.staleAfter}} {
		if setting.value != "" {
			value, err := time.ParseDuration(setting.value)
			if err != nil || value <= 0 {
				return nil, InvalidConfig.New("timeout and stale_after must be positive durations")
			}
			*setting.target = value
		}
	}
	if spec.Logger != nil {
		d.log = spec.Logger
	}
	return d, nil
}

// NewDriver constructs a driver with a bounded handshake and refresh timeout.
func NewDriver(timeout time.Duration) *Driver {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	return &Driver{timeout: timeout, staleAfter: 2 * time.Minute, log: slog.New(slog.DiscardHandler)}
}

// Baseline describes all implemented controls. A session without an operator
// print manifest removes start_print and its arguments.
func (d *Driver) Baseline() connectorapi.Set {
	return controlCapabilities()
}

// Connect authenticates, checks the reported identity and waits for initial telemetry.
// ctx bounds the handshake, not the lifetime of the returned session.
func (d *Driver) Connect(ctx context.Context, endpoint connectorapi.Endpoint, profile connectorapi.Set) (connectorapi.Session, error) {
	ctx, cancel := context.WithTimeout(ctx, d.timeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	address, err := mqttAddress(endpoint.Address)
	if err != nil {
		return nil, InvalidConfig.New(err.Error())
	}
	serial := endpoint.Options["serial"]
	if serial == "" || len(serial) > 64 || strings.IndexFunc(serial, func(r rune) bool {
		return !(r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-')
	}) >= 0 {
		return nil, InvalidConfig.New("serial must contain 1 to 64 letters, digits or hyphens")
	}
	secret, err := readSecret(endpoint.Options["access_code_file"])
	if err != nil {
		return nil, InvalidConfig.New("cannot read a nonempty access_code_file (maximum 128 bytes)")
	}
	tlsConfig, err := printerTLS(endpoint.Options)
	if err != nil {
		return nil, InvalidConfig.New(err.Error())
	}
	storage, err := storageEndpoint(address, endpoint.Options, tlsConfig)
	if err != nil {
		return nil, err
	}
	approvals, err := loadApprovals(endpoint.Options["print_manifest_file"])
	if err != nil {
		return nil, err
	}
	s := newSession(d.timeout, d.staleAfter, serial, d.Baseline().Intersect(profile), d.log)
	// Stay within signed 32-bit firmware parsers while avoiding predictable
	// sequence reuse on reconnect (including from late command replies).
	var seed [4]byte
	_, _ = rand.Read(seed[:])
	s.sequence.Store(uint64(binary.BigEndian.Uint32(seed[:]) & 0x3fffffff))
	s.storage, s.approvals = storage, approvals
	if !storage.uploads {
		delete(s.caps, connectorapi.VerbFileUpload)
	}
	if len(s.approvals) == 0 {
		delete(s.caps, VerbStartPrint)
		delete(s.caps, connectorapi.ArgumentKey(VerbStartPrint, "path"))
		delete(s.caps, connectorapi.ArgumentKey(VerbStartPrint, "tool_mapping"))
	}
	opts := mqtt.NewClientOptions().AddBroker("ssl://" + address).
		SetClientID("pandalink-" + rand.Text()[:16]).SetUsername("bblp").SetPassword(secret).
		SetTLSConfig(tlsConfig).SetProtocolVersion(4).SetCleanSession(true).
		SetAutoReconnect(false).SetConnectRetry(false).SetConnectTimeout(d.timeout).
		SetWriteTimeout(d.timeout).SetKeepAlive(30 * time.Second).SetPingTimeout(10 * time.Second).
		SetOrderMatters(true)
	opts.SetCustomOpenConnectionFn(func(_ *url.URL, _ mqtt.ClientOptions) (net.Conn, error) {
		dialer := tls.Dialer{NetDialer: &net.Dialer{Timeout: d.timeout}, Config: tlsConfig}
		conn, err := dialer.DialContext(ctx, "tcp", address)
		if err != nil {
			return nil, err
		}
		return &boundedMQTTConn{Conn: conn}, nil
	})
	opts.SetConnectionLostHandler(func(_ mqtt.Client, _ error) { s.stop() })
	opts.SetDefaultPublishHandler(func(_ mqtt.Client, msg mqtt.Message) {
		if len(msg.Payload()) > maxFrameBytes {
			s.refused.Add(1)
			s.stop()
			return
		}
		frame := append([]byte(nil), msg.Payload()...)
		select {
		case s.incoming <- receivedFrame{data: frame, at: time.Now()}:
		case <-s.done:
		default:
			// A missed delta cannot be recovered locally. Reconnect for a full report.
			s.refused.Add(1)
			s.stop()
		}
	})
	s.client = mqtt.NewClient(opts)
	go s.run()
	success := false
	defer func() {
		if !success {
			_ = s.Close()
		}
	}()
	if err = awaitToken(ctx, s.client.Connect()); err != nil {
		return nil, DeviceUnreachable.New("MQTT connection failed; check network, credentials and certificate").Wrap(err)
	}
	if err = awaitToken(ctx, s.client.Subscribe("device/"+serial+"/report", 0, nil)); err != nil {
		return nil, DeviceUnreachable.New("cannot subscribe to printer telemetry").Wrap(err)
	}
	for _, command := range []struct{ section, name string }{{"info", "get_version"}, {"pushing", "pushall"}} {
		if err = s.publish(ctx, command.section, command.name); err != nil {
			return nil, DeviceUnreachable.New("cannot request initial printer reports").Wrap(err)
		}
	}
	select {
	case answer := <-s.identified:
		if answer.err != nil {
			return nil, answer.err
		}
		s.identity = answer.identity
	case <-ctx.Done():
		return nil, DeviceUnreachable.New("printer identity timed out").Wrap(ctx.Err())
	case <-s.done:
		return nil, DeviceUnreachable.New("printer disconnected during identification")
	}
	select {
	case <-s.initialState:
	case <-ctx.Done():
		return nil, DeviceUnreachable.New("initial printer state timed out").Wrap(ctx.Err())
	case <-s.done:
		return nil, DeviceUnreachable.New("printer disconnected before initial telemetry")
	}
	success = true
	return s, nil
}

func mqttAddress(address string) (string, error) {
	if strings.ContainsAny(address, "/@?#\r\n\t ") || address == "" {
		return "", errors.New("address must be a host or host:port, without credentials or a URL scheme")
	}
	if net.ParseIP(address) != nil {
		return net.JoinHostPort(address, "8883"), nil
	}
	if !strings.Contains(address, ":") {
		return net.JoinHostPort(address, "8883"), nil
	}
	host, port, err := net.SplitHostPort(address)
	n, numberErr := strconv.Atoi(port)
	if err != nil || numberErr != nil || host == "" || n < 1 || n > 65535 {
		return "", errors.New("address must contain a valid host and port")
	}
	return address, nil
}

func readSecret(path string) (string, error) {
	return secret.Read(path)
}

func printerTLS(options map[string]string) (*tls.Config, error) {
	return printertls.Config(options)
}

func awaitToken(ctx context.Context, token mqtt.Token) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-token.Done():
		return token.Error()
	}
}

func decodeIdentity(frame []byte, serial string) (connectorapi.Identity, bool, error) {
	var report struct {
		Info struct {
			Command string `json:"command"`
			Modules []struct {
				Name     string `json:"name"`
				Product  string `json:"product_name"`
				Serial   string `json:"sn"`
				Firmware string `json:"sw_ver"`
			} `json:"module"`
		} `json:"info"`
	}
	if err := json.Unmarshal(frame, &report); err != nil {
		return connectorapi.Identity{}, false, err
	}
	if report.Info.Command != "get_version" {
		return connectorapi.Identity{}, false, nil
	}
	for _, module := range report.Info.Modules {
		if module.Name != "ota" {
			continue
		}
		if module.Serial != serial {
			return connectorapi.Identity{}, true, DeviceUnreachable.New("reported printer serial does not match the endpoint")
		}
		if module.Product != "Bambu Lab H3C" {
			return connectorapi.Identity{}, true, DeviceUnreachable.New("this release supports the H3C only")
		}
		return connectorapi.Identity{Vendor: "Bambu Lab", Model: "H3C", Serial: serial, UnitUID: "bambu:" + serial, Firmware: connectorapi.NewFirmwareLabel(module.Firmware)}, true, nil
	}
	return connectorapi.Identity{}, false, nil
}

func (s *Session) publish(ctx context.Context, section, command string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	frame, err := json.Marshal(map[string]any{section: map[string]string{"command": command, "sequence_id": fmt.Sprint(s.sequence.Add(1))}})
	if err != nil {
		return err
	}
	return awaitToken(ctx, s.client.Publish("device/"+s.serial+"/request", 0, false, frame))
}
