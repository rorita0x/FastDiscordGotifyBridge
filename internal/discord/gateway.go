// Package discord implements a minimal Discord Gateway (v10) client that
// connects with a user token and dispatches MESSAGE_CREATE events.
package discord

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const gatewayURL = "wss://gateway.discord.gg/?v=10&encoding=json"

// Gateway opcodes (https://discord.com/developers/docs/topics/gateway-events).
const (
	opDispatch       = 0
	opHeartbeat      = 1
	opIdentify       = 2
	opResume         = 6
	opReconnect      = 7
	opInvalidSession = 9
	opHello          = 10
	opHeartbeatACK   = 11
)

// ErrAuthFailed is returned when Discord rejects the token (close code 4004).
var ErrAuthFailed = errors.New("discord authentication failed (invalid user token)")

// Message is a normalized MESSAGE_CREATE event handed to the Handler.
type Message struct {
	ID          string
	ChannelID   string
	GuildID     string
	AuthorID    string
	AuthorName  string
	AuthorBot   bool
	Content     string
	Attachments []string
}

// Handler is invoked for every forwarded message.
type Handler func(Message)

// Options configures a Gateway.
type Options struct {
	Token     string
	Handler   Handler
	NotifyOwn bool
	RootCAs   *x509.CertPool
	Logger    *slog.Logger
}

// Gateway maintains a resilient connection to the Discord gateway.
type Gateway struct {
	opts Options
	log  *slog.Logger

	conn    *websocket.Conn
	writeMu sync.Mutex

	mu        sync.Mutex
	seq       int64
	hasSeq    bool
	sessionID string
	resumeURL string
	selfID    string
	acked     bool
}

// New creates a Gateway from the given options.
func New(opts Options) *Gateway {
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Gateway{opts: opts, log: log}
}

type payload struct {
	Op int             `json:"op"`
	D  json.RawMessage `json:"d"`
	S  *int64          `json:"s"`
	T  string          `json:"t"`
}

// Run connects and keeps the gateway alive, reconnecting (and resuming when
// possible) until ctx is cancelled or authentication fails fatally.
func (g *Gateway) Run(ctx context.Context) error {
	backoff := time.Second
	const maxBackoff = 60 * time.Second

	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		start := time.Now()
		err := g.connect(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if errors.Is(err, ErrAuthFailed) {
			return err
		}
		if err != nil {
			g.log.Warn("gateway disconnected", "err", err)
		}

		// A long-lived connection means things were healthy: reset backoff.
		if time.Since(start) > 30*time.Second {
			backoff = time.Second
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		if backoff *= 2; backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

// connect performs a single connect/identify-or-resume/read cycle.
func (g *Gateway) connect(ctx context.Context) error {
	dialer := websocket.Dialer{HandshakeTimeout: 30 * time.Second}
	if g.opts.RootCAs != nil {
		dialer.TLSClientConfig = &tls.Config{RootCAs: g.opts.RootCAs}
	}

	g.mu.Lock()
	resuming := g.sessionID != "" && g.resumeURL != ""
	url := gatewayURL
	if resuming {
		url = g.resumeURL + "/?v=10&encoding=json"
	}
	g.mu.Unlock()

	conn, _, err := dialer.DialContext(ctx, url, nil)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer conn.Close()
	g.conn = conn

	// First frame must be Hello.
	var hello payload
	if err := conn.ReadJSON(&hello); err != nil {
		return fmt.Errorf("read hello: %w", err)
	}
	if hello.Op != opHello {
		return fmt.Errorf("expected hello (op 10), got op %d", hello.Op)
	}
	var helloData struct {
		HeartbeatInterval int `json:"heartbeat_interval"`
	}
	if err := json.Unmarshal(hello.D, &helloData); err != nil {
		return fmt.Errorf("decode hello: %w", err)
	}
	interval := time.Duration(helloData.HeartbeatInterval) * time.Millisecond

	g.mu.Lock()
	g.acked = true
	g.mu.Unlock()

	connCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go g.heartbeatLoop(connCtx, interval)

	if resuming {
		g.log.Info("resuming session")
		if err := g.sendResume(); err != nil {
			return fmt.Errorf("send resume: %w", err)
		}
	} else {
		g.log.Info("identifying")
		if err := g.sendIdentify(); err != nil {
			return fmt.Errorf("send identify: %w", err)
		}
	}

	return g.readLoop(conn)
}

func (g *Gateway) readLoop(conn *websocket.Conn) error {
	for {
		var p payload
		if err := conn.ReadJSON(&p); err != nil {
			return g.classifyCloseErr(err)
		}
		if p.S != nil {
			g.mu.Lock()
			g.seq = *p.S
			g.hasSeq = true
			g.mu.Unlock()
		}

		switch p.Op {
		case opDispatch:
			g.handleDispatch(p)
		case opHeartbeat:
			// Server asked us to heartbeat immediately.
			if err := g.sendHeartbeat(); err != nil {
				return err
			}
		case opHeartbeatACK:
			g.mu.Lock()
			g.acked = true
			g.mu.Unlock()
		case opReconnect:
			g.log.Info("server requested reconnect")
			return errors.New("reconnect requested")
		case opInvalidSession:
			var resumable bool
			_ = json.Unmarshal(p.D, &resumable)
			g.log.Warn("invalid session", "resumable", resumable)
			if !resumable {
				g.clearSession()
			}
			return errors.New("invalid session")
		}
	}
}

func (g *Gateway) handleDispatch(p payload) {
	switch p.T {
	case "READY":
		var ready struct {
			SessionID        string `json:"session_id"`
			ResumeGatewayURL string `json:"resume_gateway_url"`
			User             struct {
				ID       string `json:"id"`
				Username string `json:"username"`
			} `json:"user"`
		}
		if err := json.Unmarshal(p.D, &ready); err != nil {
			g.log.Warn("decode READY", "err", err)
			return
		}
		g.mu.Lock()
		g.sessionID = ready.SessionID
		g.resumeURL = ready.ResumeGatewayURL
		g.selfID = ready.User.ID
		g.mu.Unlock()
		g.log.Info("ready", "user", ready.User.Username, "user_id", ready.User.ID)

	case "RESUMED":
		g.log.Info("resumed")

	case "MESSAGE_CREATE":
		g.handleMessageCreate(p.D)
	}
}

type rawMessage struct {
	ID        string `json:"id"`
	ChannelID string `json:"channel_id"`
	GuildID   string `json:"guild_id"`
	Content   string `json:"content"`
	Author    struct {
		ID         string `json:"id"`
		Username   string `json:"username"`
		GlobalName string `json:"global_name"`
		Bot        bool   `json:"bot"`
	} `json:"author"`
	Attachments []struct {
		URL      string `json:"url"`
		Filename string `json:"filename"`
	} `json:"attachments"`
}

func (g *Gateway) handleMessageCreate(d json.RawMessage) {
	var rm rawMessage
	if err := json.Unmarshal(d, &rm); err != nil {
		g.log.Warn("decode MESSAGE_CREATE", "err", err)
		return
	}

	g.mu.Lock()
	self := g.selfID
	g.mu.Unlock()
	if !g.opts.NotifyOwn && rm.Author.ID == self {
		return
	}

	name := rm.Author.GlobalName
	if name == "" {
		name = rm.Author.Username
	}

	msg := Message{
		ID:         rm.ID,
		ChannelID:  rm.ChannelID,
		GuildID:    rm.GuildID,
		AuthorID:   rm.Author.ID,
		AuthorName: name,
		AuthorBot:  rm.Author.Bot,
		Content:    rm.Content,
	}
	for _, a := range rm.Attachments {
		msg.Attachments = append(msg.Attachments, a.URL)
	}

	if g.opts.Handler != nil {
		g.opts.Handler(msg)
	}
}

// --- sending ---

func (g *Gateway) send(v any) error {
	g.writeMu.Lock()
	defer g.writeMu.Unlock()
	return g.conn.WriteJSON(v)
}

func (g *Gateway) sendHeartbeat() error {
	g.mu.Lock()
	var seq *int64
	if g.hasSeq {
		s := g.seq
		seq = &s
	}
	g.mu.Unlock()
	return g.send(map[string]any{"op": opHeartbeat, "d": seq})
}

func (g *Gateway) sendResume() error {
	g.mu.Lock()
	d := map[string]any{
		"token":      g.opts.Token,
		"session_id": g.sessionID,
		"seq":        g.seq,
	}
	g.mu.Unlock()
	return g.send(map[string]any{"op": opResume, "d": d})
}

// sendIdentify sends a user-account IDENTIFY mimicking the official web client.
func (g *Gateway) sendIdentify() error {
	identify := map[string]any{
		"op": opIdentify,
		"d": map[string]any{
			"token":        g.opts.Token,
			"capabilities": 30717,
			"properties": map[string]any{
				"os":                     "Linux",
				"browser":                "Firefox",
				"device":                 "",
				"system_locale":          "en-US",
				"browser_user_agent":     "Mozilla/5.0 (X11; Linux x86_64; rv:125.0) Gecko/20100101 Firefox/125.0",
				"browser_version":        "125.0",
				"os_version":             "",
				"referrer":               "",
				"referring_domain":       "",
				"referrer_current":       "",
				"referring_domain_current": "",
				"release_channel":        "stable",
				"client_build_number":    300000,
				"client_event_source":    nil,
			},
			"presence": map[string]any{
				"status":     "online",
				"since":      0,
				"activities": []any{},
				"afk":        false,
			},
			"compress": false,
			"client_state": map[string]any{
				"guild_versions":             map[string]any{},
				"highest_last_message_id":    "0",
				"read_state_version":         0,
				"user_guild_settings_version": -1,
				"user_settings_version":      -1,
				"private_channels_version":   "0",
				"api_code_version":           0,
			},
		},
	}
	return g.send(identify)
}

// --- heartbeat / liveness ---

func (g *Gateway) heartbeatLoop(ctx context.Context, interval time.Duration) {
	// Discord recommends jittering the first heartbeat.
	timer := time.NewTimer(interval / 2)
	defer timer.Stop()

	first := true
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}

		if !first {
			// If the previous heartbeat was never ACKed, the connection is a
			// zombie: drop it so the read loop errors and we reconnect.
			g.mu.Lock()
			acked := g.acked
			g.mu.Unlock()
			if !acked {
				g.log.Warn("no heartbeat ACK; reconnecting")
				_ = g.conn.Close()
				return
			}
		}

		g.mu.Lock()
		g.acked = false
		g.mu.Unlock()

		if err := g.sendHeartbeat(); err != nil {
			g.log.Debug("heartbeat send failed", "err", err)
			return
		}
		first = false
		timer.Reset(interval)
	}
}

// --- helpers ---

func (g *Gateway) clearSession() {
	g.mu.Lock()
	g.sessionID = ""
	g.resumeURL = ""
	g.seq = 0
	g.hasSeq = false
	g.mu.Unlock()
}

// classifyCloseErr maps Discord close codes to fatal vs. recoverable errors and
// clears the session for codes that cannot be resumed.
func (g *Gateway) classifyCloseErr(err error) error {
	var ce *websocket.CloseError
	if !errors.As(err, &ce) {
		return err
	}
	switch ce.Code {
	case 4004, 4010, 4011, 4012, 4013, 4014:
		// Authentication failed / unrecoverable identify errors.
		return fmt.Errorf("%w (close code %d)", ErrAuthFailed, ce.Code)
	case 4007, 4009:
		// Invalid seq / session timed out: reconnect fresh.
		g.clearSession()
	}
	return fmt.Errorf("websocket closed: %w", err)
}
