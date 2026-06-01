// Package gotify is a minimal client for posting messages to a Gotify server.
package gotify

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client posts notifications to a Gotify server's /message endpoint.
type Client struct {
	url   string
	token string
	http  *http.Client
}

// Message is a Gotify notification payload.
type Message struct {
	Title    string `json:"title"`
	Message  string `json:"message"`
	Priority int    `json:"priority"`
}

// New creates a Gotify client. If roots is non-nil it is used as the TLS root
// CA pool; otherwise the system pool is used.
func New(baseURL, token string, roots *x509.CertPool) *Client {
	tr := &http.Transport{
		ForceAttemptHTTP2:   true,
		MaxIdleConns:        10,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
	}
	if roots != nil {
		tr.TLSClientConfig = &tls.Config{RootCAs: roots}
	}
	return &Client{
		url:   strings.TrimRight(baseURL, "/"),
		token: token,
		http:  &http.Client{Timeout: 15 * time.Second, Transport: tr},
	}
}

// Send posts a single message to Gotify.
func (c *Client) Send(ctx context.Context, m Message) error {
	body, err := json.Marshal(m)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url+"/message", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Gotify-Key", c.token)

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return fmt.Errorf("gotify returned %s: %s", resp.Status, strings.TrimSpace(string(snippet)))
	}
	return nil
}
