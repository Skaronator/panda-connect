// pandalink-probe reads a printer without sending motion, heater or print commands.
package main

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"time"

	"github.com/prusa3d/prusa-connect/sdk/connectorapi"
	"github.com/skaronator/pandalink/connector"
)

func main() {
	address := flag.String("address", os.Getenv("BAMBU_ADDRESS"), "printer host:port")
	serial := flag.String("serial", os.Getenv("BAMBU_SERIAL"), "printer serial")
	codeFile := flag.String("access-code-file", "/run/secrets/bambu_access_code", "file containing the access code")
	pin := flag.String("tls-fingerprint", os.Getenv("BAMBU_TLS_FINGERPRINT"), "SHA-256 certificate fingerprint")
	inspect := flag.Bool("inspect-certificate", false, "inspect the unauthenticated certificate without sending credentials")
	duration := flag.Duration("duration", 10*time.Second, "how long to read telemetry after the handshake")
	flag.Parse()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	var err error
	if *inspect {
		err = inspectCertificate(ctx, *address)
	} else {
		err = probe(ctx, *address, *serial, *codeFile, *pin, *duration)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func inspectCertificate(ctx context.Context, address string) error {
	if _, _, err := net.SplitHostPort(address); err != nil {
		address = net.JoinHostPort(address, "8883")
	}
	// Enrollment only: no MQTT authentication or printer commands on this socket.
	dialer := tls.Dialer{NetDialer: &net.Dialer{Timeout: 10 * time.Second}, Config: &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12, MaxVersion: tls.VersionTLS12}}
	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return fmt.Errorf("inspect printer certificate: %w", err)
	}
	defer conn.Close()
	cert := conn.(*tls.Conn).ConnectionState().PeerCertificates[0]
	sum := sha256.Sum256(cert.Raw)
	fmt.Println("Unverified certificate; check the printer address and verify this fingerprint before trusting it.")
	fmt.Println("Subject:", cert.Subject)
	fmt.Println("BAMBU_TLS_FINGERPRINT=" + hex.EncodeToString(sum[:]))
	return nil
}

func probe(ctx context.Context, address, serial, codeFile, pin string, duration time.Duration) error {
	driver := connector.NewDriver(0)
	session, err := driver.Connect(ctx, connectorapi.Endpoint{Address: address, Options: map[string]string{
		"serial": serial, "access_code_file": codeFile, "tls_fingerprint": pin,
	}}, driver.Baseline())
	if err != nil {
		return err
	}
	defer session.Close()
	enc := json.NewEncoder(os.Stdout)
	if err = enc.Encode(session.Identity()); err != nil {
		return err
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return nil
		case patch, open := <-session.State():
			if !open {
				return fmt.Errorf("printer session ended")
			}
			if err = enc.Encode(patch); err != nil {
				return err
			}
		}
	}
}
