// Package printertls implements the explicit certificate trust shared by MQTT,
// FTPS and the local camera connection. There is no unverified connection mode.
package printertls

import (
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"os"
	"strings"
)

func Config(options map[string]string) (*tls.Config, error) {
	cfg := &tls.Config{MinVersion: tls.VersionTLS12, MaxVersion: tls.VersionTLS12, ServerName: options["tls_server_name"]}
	if fingerprint := options["tls_fingerprint"]; fingerprint != "" {
		pin, err := hex.DecodeString(strings.ReplaceAll(fingerprint, ":", ""))
		if err != nil || len(pin) != sha256.Size {
			return nil, errors.New("tls_fingerprint must be a SHA-256 certificate fingerprint")
		}
		// Exact pin validation replaces public-CA and hostname validation.
		cfg.InsecureSkipVerify = true
		cfg.VerifyConnection = func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return errors.New("printer sent no certificate")
			}
			sum := sha256.Sum256(cs.PeerCertificates[0].Raw)
			if subtle.ConstantTimeCompare(sum[:], pin) != 1 {
				return errors.New("printer TLS certificate changed; verify its new fingerprint")
			}
			return nil
		}
	} else if path := options["ca_file"]; path != "" {
		pem, err := os.ReadFile(path)
		if err != nil {
			return nil, errors.New("cannot read ca_file")
		}
		cfg.RootCAs = x509.NewCertPool()
		if !cfg.RootCAs.AppendCertsFromPEM(pem) {
			return nil, errors.New("ca_file contains no certificates")
		}
	} else {
		return nil, errors.New("set tls_fingerprint or ca_file; unverified TLS is not supported")
	}
	return cfg, nil
}
