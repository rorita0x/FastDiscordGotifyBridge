//go:build embedcerts

// Package certs optionally embeds a CA bundle into the binary so the program
// can perform TLS without any files present on disk (e.g. a scratch image).
package certs

import (
	"crypto/x509"
	_ "embed"
	"fmt"
)

// cacert.pem is fetched and placed here by the Docker build before compiling
// with -tags embedcerts. It is intentionally not committed to the repository.
//
//go:embed cacert.pem
var caBundle []byte

// Pool returns a certificate pool built from the embedded CA bundle.
func Pool() (*x509.CertPool, error) {
	p := x509.NewCertPool()
	if !p.AppendCertsFromPEM(caBundle) {
		return nil, fmt.Errorf("failed to parse embedded CA bundle")
	}
	return p, nil
}
