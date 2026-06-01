//go:build !embedcerts

package certs

import "crypto/x509"

// Pool returns nil, which makes the HTTP client and WebSocket dialer fall back
// to the host's system certificate pool. Used for normal (non-scratch) builds.
func Pool() (*x509.CertPool, error) {
	return nil, nil
}
