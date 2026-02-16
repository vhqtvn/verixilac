package vhwebhook

import (
	"encoding/json"
	"math"
	"math/rand"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

// WebhookRequest represents an incoming webhook request from the VH ingress.
type WebhookRequest struct {
	Type    string            `json:"type"`
	ID      string            `json:"id"`
	Headers map[string]string `json:"headers"`
	BodyB64 string            `json:"body_b64"`
	Method  string            `json:"method,omitempty"`
	Path    string            `json:"path,omitempty"`
}

// Options configures the VHWebHook client.
type Options struct {
	BackoffInitialMs   int     // Initial backoff in ms (default 250)
	BackoffMaxMs       int     // Max backoff in ms (default 30000)
	BackoffJitter      float64 // Random jitter fraction (default 0.2 = ±20%)
	MaxQueue           int     // Max queued messages while disconnected (default 500)
	ProxyChunking      bool    // Handshake option
	HeartbeatTimeoutMs int     // Heartbeat timeout in ms (default 60000)
}

func (o *Options) withDefaults() Options {
	out := *o
	if out.BackoffInitialMs <= 0 {
		out.BackoffInitialMs = 250
	}
	if out.BackoffMaxMs <= 0 {
		out.BackoffMaxMs = 30_000
	}
	if out.BackoffJitter <= 0 {
		out.BackoffJitter = 0.2
	}
	if out.MaxQueue <= 0 {
		out.MaxQueue = 500
	}
	if out.HeartbeatTimeoutMs <= 0 {
		out.HeartbeatTimeoutMs = 60_000
	}
	return out
}

type outgoingMsg struct {
	Type        string      `json:"type"`
	Application string      `json:"application,omitempty"`
	Token       string      `json:"token,omitempty"`
	Options     *msgOptions `json:"options,omitempty"`
	ID          string      `json:"id,omitempty"`
	Data        interface{} `json:"data,omitempty"`
}

type msgOptions struct {
	ProxyChunking bool `json:"proxy_chunking,omitempty"`
}

// VHWebHook manages a persistent WebSocket connection to a VH ingress proxy.
type VHWebHook struct {
	url      string
	appName  string
	appToken string
	opts     Options

	log zerolog.Logger

	mu             sync.Mutex
	ws             *websocket.Conn
	stopped        bool
	connecting     bool
	reconnectTimer *time.Timer
	heartbeatTimer *time.Timer
	backoffMs      int
	queue          []string
	stopCh         chan struct{}

	handler func(WebhookRequest)
}

// New creates a new VHWebHook client.
func New(rawURL, appName, appToken string, opts Options) *VHWebHook {
	opts = opts.withDefaults()
	return &VHWebHook{
		url:       rawURL,
		appName:   appName,
		appToken:  appToken,
		opts:      opts,
		log:       log.With().Str("component", "vhwebhook").Logger(),
		stopped:   true,
		backoffMs: opts.BackoffInitialMs,
	}
}

// OnWebhookRequest sets the handler for incoming webhook requests.
func (v *VHWebHook) OnWebhookRequest(handler func(WebhookRequest)) {
	v.handler = handler
}

// Start begins connecting (idempotent).
func (v *VHWebHook) Start() {
	v.mu.Lock()
	defer v.mu.Unlock()

	if !v.stopped {
		return
	}
	v.stopped = false
	v.stopCh = make(chan struct{})
	v.scheduleConnect(0)
}

// Stop disconnects and prevents reconnecting (idempotent).
func (v *VHWebHook) Stop() {
	v.mu.Lock()
	defer v.mu.Unlock()

	if v.stopped {
		return
	}
	v.stopped = true
	v.connecting = false
	close(v.stopCh)

	v.clearHeartbeat()

	if v.reconnectTimer != nil {
		v.reconnectTimer.Stop()
		v.reconnectTimer = nil
	}

	ws := v.ws
	v.ws = nil

	if ws != nil {
		_ = ws.Close()
	}

	v.queue = nil
	v.backoffMs = v.opts.BackoffInitialMs
}

// send marshals and sends a message, or queues it if disconnected.
func (v *VHWebHook) send(msg outgoingMsg) {
	data, err := json.Marshal(msg)
	if err != nil {
		v.log.Error().Err(err).Msg("failed to marshal message")
		return
	}

	v.mu.Lock()
	defer v.mu.Unlock()

	if v.ws != nil {
		if err := v.ws.WriteMessage(websocket.TextMessage, data); err != nil {
			v.log.Error().Err(err).Msg("failed to send message")
		}
		return
	}

	// Queue with cap
	if len(v.queue) >= v.opts.MaxQueue {
		v.queue = v.queue[1:]
		v.log.Warn().Msg("send queue full; dropping oldest message")
	}
	v.queue = append(v.queue, string(data))
}

// scheduleConnect schedules a connection attempt after delayMs.
// Must be called with v.mu held.
func (v *VHWebHook) scheduleConnect(delayMs int) {
	if v.stopped {
		return
	}
	if v.reconnectTimer != nil {
		return // already scheduled
	}
	v.reconnectTimer = time.AfterFunc(time.Duration(delayMs)*time.Millisecond, func() {
		v.mu.Lock()
		v.reconnectTimer = nil
		v.mu.Unlock()
		v.connect()
	})
}

func (v *VHWebHook) jittered(ms int) int {
	j := v.opts.BackoffJitter
	if j <= 0 {
		return ms
	}
	delta := float64(ms) * j
	r := (rand.Float64()*2 - 1) * delta
	result := int(math.Round(float64(ms) + r))
	if result < 0 {
		return 0
	}
	return result
}

func (v *VHWebHook) bumpBackoff() {
	v.backoffMs = int(math.Min(float64(v.opts.BackoffMaxMs),
		math.Max(float64(v.opts.BackoffInitialMs), float64(v.backoffMs)*2)))
}

func (v *VHWebHook) resetBackoff() {
	v.backoffMs = v.opts.BackoffInitialMs
}

func (v *VHWebHook) handleConnectionClose(ws *websocket.Conn, why string, err error) {
	v.mu.Lock()
	defer v.mu.Unlock()

	if v.ws != ws {
		return // stale socket
	}

	v.clearHeartbeat()

	if v.stopped {
		return
	}

	if err != nil {
		v.log.Error().Err(err).Msg(why)
	} else {
		v.log.Warn().Msg(why)
	}

	v.ws = nil
	v.connecting = false

	_ = ws.Close()

	delay := v.jittered(v.backoffMs)
	v.bumpBackoff()
	v.scheduleConnect(delay)
}

func (v *VHWebHook) connect() {
	v.mu.Lock()
	if v.stopped || v.connecting {
		v.mu.Unlock()
		return
	}
	v.connecting = true
	v.mu.Unlock()

	connectURL := v.buildConnectURL()

	v.log.Info().Str("url", connectURL).Msg("connecting")

	dialer := websocket.DefaultDialer
	ws, _, err := dialer.Dial(connectURL, nil)
	if err != nil {
		v.mu.Lock()
		v.connecting = false
		delay := v.jittered(v.backoffMs)
		v.bumpBackoff()
		v.scheduleConnect(delay)
		v.mu.Unlock()
		v.log.Error().Err(err).Msg("failed to connect")
		return
	}

	v.mu.Lock()
	v.ws = ws
	v.connecting = false
	v.resetBackoff()
	v.mu.Unlock()

	v.log.Info().Str("url", v.url).Msg("connected")

	// Start heartbeat
	v.resetHeartbeat(ws)

	// Send handshake
	v.send(outgoingMsg{
		Type:        "handshake",
		Application: v.appName,
		Token:       v.appToken,
		Options:     &msgOptions{ProxyChunking: v.opts.ProxyChunking},
	})

	// Flush queued messages
	v.flushQueue()

	// Read loop
	v.readLoop(ws)
}

func (v *VHWebHook) buildConnectURL() string {
	u := v.url

	// Convert http(s) to ws(s)
	if strings.HasPrefix(u, "http://") {
		u = "ws://" + u[len("http://"):]
	} else if strings.HasPrefix(u, "https://") {
		u = "wss://" + u[len("https://"):]
	}

	// Ensure it's a valid URL; if no scheme, assume wss
	if !strings.HasPrefix(u, "ws://") && !strings.HasPrefix(u, "wss://") {
		u = "wss://" + u
	}

	u = strings.TrimRight(u, "/")

	// Parse and append /connect path
	parsed, err := url.Parse(u)
	if err != nil {
		return u + "/connect"
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/connect"
	return parsed.String()
}

func (v *VHWebHook) readLoop(ws *websocket.Conn) {
	for {
		_, message, err := ws.ReadMessage()
		if err != nil {
			v.handleConnectionClose(ws, "WebSocket read error", err)
			return
		}

		// Any message is a heartbeat
		v.resetHeartbeat(ws)

		v.handleRawMessage(ws, message)
	}
}

func (v *VHWebHook) handleRawMessage(ws *websocket.Conn, data []byte) {
	var msg struct {
		Type string          `json:"type"`
		ID   string          `json:"id"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(data, &msg); err != nil {
		v.log.Error().Err(err).Msg("failed to parse JSON message")
		return
	}

	switch msg.Type {
	case "webhook_request":
		// ACK quickly
		v.send(outgoingMsg{Type: "webhook_ack", ID: msg.ID})

		// Parse the full request
		var req WebhookRequest
		if err := json.Unmarshal(data, &req); err != nil {
			v.log.Error().Err(err).Msg("failed to parse webhook request")
			return
		}

		// Run handler async
		if v.handler != nil {
			go func() {
				defer func() {
					if r := recover(); r != nil {
						v.log.Error().Interface("panic", r).Msg("webhook handler panicked")
					}
				}()
				v.handler(req)
			}()
		}

	case "ping":
		v.send(outgoingMsg{Type: "pong", Data: msg.Data})

	default:
		v.log.Debug().Str("type", msg.Type).Msg("unhandled message type")
	}
}

func (v *VHWebHook) resetHeartbeat(ws *websocket.Conn) {
	v.mu.Lock()
	defer v.mu.Unlock()

	v.clearHeartbeat()
	v.heartbeatTimer = time.AfterFunc(
		time.Duration(v.opts.HeartbeatTimeoutMs)*time.Millisecond,
		func() {
			v.handleConnectionClose(ws, "heartbeat timeout; terminating connection", nil)
		},
	)
}

// clearHeartbeat stops the heartbeat timer. Must be called with v.mu held.
func (v *VHWebHook) clearHeartbeat() {
	if v.heartbeatTimer != nil {
		v.heartbeatTimer.Stop()
		v.heartbeatTimer = nil
	}
}

func (v *VHWebHook) flushQueue() {
	v.mu.Lock()
	defer v.mu.Unlock()

	if v.ws == nil {
		return
	}

	for _, msg := range v.queue {
		if err := v.ws.WriteMessage(websocket.TextMessage, []byte(msg)); err != nil {
			v.log.Error().Err(err).Msg("failed to flush queued message")
			break
		}
	}
	v.queue = nil
}
