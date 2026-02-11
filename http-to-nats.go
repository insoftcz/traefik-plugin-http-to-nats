package traefik_plugin_http_to_nats

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Config holds the plugin configuration.
type Config struct {
	NatsUrl        string `json:"natsUrl,omitempty"`
	SubjectPattern string `json:"subjectPattern,omitempty"`
	Timeout        int    `json:"timeout,omitempty"`
	Username       string `json:"username,omitempty"`
	Password       string `json:"password,omitempty"`
	Token          string `json:"token,omitempty"`
}

// CreateConfig creates and initializes the plugin configuration.
func CreateConfig() *Config {
	return &Config{
		NatsUrl:        "nats://localhost:4222",
		SubjectPattern: "/api/{subject}",
		Timeout:        5000, // milliseconds
	}
}

// HttpToNats holds the plugin instance.
type HttpToNats struct {
	next           http.Handler
	name           string
	natsClient     *NatsClient
	natsConfig     *natsConfig
	subjectPattern string
	pathRegex      *regexp.Regexp
	timeout        time.Duration
	mu             sync.Mutex
}

// natsConfig holds the configuration needed to create a NATS connection
type natsConfig struct {
	host     string
	username string
	password string
	token    string
}

// New creates a new HttpToNats plugin instance.
func New(ctx context.Context, next http.Handler, config *Config, name string) (http.Handler, error) {
	if config.NatsUrl == "" {
		return nil, fmt.Errorf("natsUrl cannot be empty")
	}

	if config.SubjectPattern == "" {
		return nil, fmt.Errorf("subjectPattern cannot be empty")
	}

	// Convert subject pattern to regex
	pathRegex, err := buildPathRegex(config.SubjectPattern)
	if err != nil {
		return nil, fmt.Errorf("invalid subject pattern: %w", err)
	}

	// Parse NATS URL
	natsURL, err := url.Parse(config.NatsUrl)
	if err != nil {
		return nil, fmt.Errorf("invalid NATS URL: %w", err)
	}

	timeout := time.Duration(config.Timeout) * time.Millisecond
	if timeout == 0 {
		timeout = 5 * time.Second
	}

	// Store NATS configuration for lazy connection
	natsConf := &natsConfig{
		host:     natsURL.Host,
		username: config.Username,
		password: config.Password,
		token:    config.Token,
	}

	return &HttpToNats{
		next:           next,
		name:           name,
		natsClient:     nil, // Will be initialized lazily
		natsConfig:     natsConf,
		subjectPattern: config.SubjectPattern,
		pathRegex:      pathRegex,
		timeout:        timeout,
	}, nil
}

// buildPathRegex converts a pattern like "/api/{subject}" to a regex that captures the dynamic part.
func buildPathRegex(pattern string) (*regexp.Regexp, error) {
	// Escape regex special characters except for our placeholders
	escaped := regexp.QuoteMeta(pattern)

	// Replace escaped placeholders with named capture groups
	// Pattern: \{(\w+)\} -> named group
	regexPattern := regexp.MustCompile(`\\{(\w+)\\}`).ReplaceAllString(escaped, `(?P<$1>[^/]+)`)

	// Add anchors to match full path
	regexPattern = "^" + regexPattern + "$"

	return regexp.Compile(regexPattern)
}

// extractSubject extracts the dynamic part from the URL path based on the pattern.
func (h *HttpToNats) extractSubject(path string) (string, error) {
	matches := h.pathRegex.FindStringSubmatch(path)
	if matches == nil {
		return "", fmt.Errorf("path does not match pattern")
	}

	// Find the named groups
	names := h.pathRegex.SubexpNames()
	result := make(map[string]string)

	for i, name := range names {
		if i != 0 && name != "" {
			result[name] = matches[i]
		}
	}

	// Get the 'subject' group (or first named group if 'subject' doesn't exist)
	if subject, ok := result["subject"]; ok {
		return subject, nil
	}

	// If no 'subject' named group, return the first captured group
	for _, value := range result {
		return value, nil
	}

	return "", fmt.Errorf("no subject found in path")
}

// NatsRequest represents the request data sent to NATS.
type NatsRequest struct {
	Method  string            `json:"method"`
	Path    string            `json:"path"`
	Headers map[string]string `json:"headers"`
	Query   map[string]string `json:"query"`
	Body    string            `json:"body,omitempty"`
}

// NatsResponse represents the response received from NATS.
type NatsResponse struct {
	StatusCode int               `json:"statusCode"`
	Headers    map[string]string `json:"headers,omitempty"`
	Body       string            `json:"body,omitempty"`
}

// ensureConnected ensures the NATS client is connected (lazy initialization and reconnection)
func (h *HttpToNats) ensureConnected() error {
	h.mu.Lock()
	defer h.mu.Unlock()

	// Check if we have a client and if it's connected
	if h.natsClient != nil && h.natsClient.IsConnected() {
		return nil
	}

	// Close old connection if exists but disconnected
	if h.natsClient != nil {
		h.natsClient.Close()
		h.natsClient = nil
	}

	// Create NATS client connection with retry logic
	var client *NatsClient
	var err error
	maxRetries := 3
	baseDelay := 100 * time.Millisecond

	for attempt := 0; attempt < maxRetries; attempt++ {
		if attempt > 0 {
			delay := baseDelay * time.Duration(1<<uint(attempt-1)) // Exponential backoff: 100ms, 200ms, 400ms
			fmt.Printf("[NATS] Reconnection attempt %d/%d after %v\n", attempt+1, maxRetries, delay)
			time.Sleep(delay)
		}

		client, err = NewNatsClient(h.natsConfig.host, h.natsConfig.username, h.natsConfig.password, h.natsConfig.token)
		if err == nil {
			h.natsClient = client
			fmt.Println("[NATS] Successfully connected/reconnected")
			return nil
		}
		fmt.Printf("[NATS] Connection attempt %d failed: %v\n", attempt+1, err)
	}

	return fmt.Errorf("failed to connect to NATS after %d attempts: %w", maxRetries, err)
}

// ServeHTTP handles the HTTP request and forwards it to NATS.
func (h *HttpToNats) ServeHTTP(rw http.ResponseWriter, req *http.Request) {
	// Ensure NATS connection is established (lazy initialization and reconnection)
	if err := h.ensureConnected(); err != nil {
		http.Error(rw, fmt.Sprintf("Failed to connect to NATS: %v", err), http.StatusServiceUnavailable)
		return
	}

	// Double-check connection before making request
	if !h.natsClient.IsConnected() {
		http.Error(rw, "NATS connection lost", http.StatusServiceUnavailable)
		return
	}

	// Extract subject from URL
	subject, err := h.extractSubject(req.URL.Path)
	if err != nil {
		http.Error(rw, fmt.Sprintf("Invalid path: %v", err), http.StatusBadRequest)
		return
	}

	// Read request body
	bodyBytes, err := io.ReadAll(req.Body)
	if err != nil {
		http.Error(rw, "Failed to read request body", http.StatusInternalServerError)
		return
	}
	defer req.Body.Close()

	// Restore body for potential next handler
	req.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))

	// Send only the raw body to NATS and wait for response
	respData, err := h.natsClient.Request(subject, bodyBytes, h.timeout)
	if err != nil {
		// If connection was lost, mark client for reconnection
		if !h.natsClient.IsConnected() {
			h.mu.Lock()
			if h.natsClient != nil {
				h.natsClient.Close()
				h.natsClient = nil
			}
			h.mu.Unlock()
		}

		if err.Error() == "timeout" {
			http.Error(rw, "NATS request timeout", http.StatusGatewayTimeout)
		} else if err.Error() == "not connected to NATS" {
			http.Error(rw, "NATS connection lost", http.StatusServiceUnavailable)
		} else {
			http.Error(rw, fmt.Sprintf("NATS request failed: %v", err), http.StatusBadGateway)
		}
		return
	}

	// Parse NATS response - expecting tuple of (status code, data)
	var respTuple []interface{}
	if err := json.Unmarshal(respData, &respTuple); err != nil {
		http.Error(rw, "Failed to parse NATS response", http.StatusBadGateway)
		return
	}

	// Validate tuple format
	if len(respTuple) != 2 {
		http.Error(rw, "Invalid NATS response format: expected tuple of (statusCode, data)", http.StatusBadGateway)
		return
	}

	// Extract status code
	statusCode, ok := respTuple[0].(float64) // JSON numbers unmarshal as float64
	if !ok {
		http.Error(rw, "Invalid status code in NATS response", http.StatusBadGateway)
		return
	}

	// Extract data (can be any type)
	data := respTuple[1]

	// Set JSON content type for all responses
	rw.Header().Set("Content-Type", "application/json")

	if statusCode == 1 {
		// Write status code
		rw.WriteHeader(200)
	} else {
		http.Error(rw, fmt.Sprintf("Error: %d", int(statusCode)), http.StatusBadRequest)
		return
	}

	// Write response body based on data type
	switch v := data.(type) {
	case string:
		rw.Write([]byte(v))
	case []byte:
		rw.Write(v)
	case nil:
		// No body
	default:
		// For other types, marshal back to JSON
		jsonData, err := json.Marshal(v)
		if err != nil {
			http.Error(rw, "Failed to marshal response data", http.StatusInternalServerError)
			return
		}
		rw.Write(jsonData)
	}
}

// NatsClient implements a basic NATS client using the NATS protocol
type NatsClient struct {
	conn          net.Conn
	reader        *bufio.Reader
	writer        *bufio.Writer
	mu            sync.Mutex
	subscriptions map[string]chan []byte
	inboxPrefix   string
	connected     atomic.Bool
	sidCounter    uint64
	closeChan     chan struct{}
}

// NewNatsClient creates a new NATS client connection
func NewNatsClient(addr, username, password, token string) (*NatsClient, error) {
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to NATS: %w", err)
	}

	client := &NatsClient{
		conn:          conn,
		reader:        bufio.NewReader(conn),
		writer:        bufio.NewWriter(conn),
		subscriptions: make(map[string]chan []byte),
		inboxPrefix:   "_INBOX.",
		sidCounter:    0,
		closeChan:     make(chan struct{}),
	}
	client.connected.Store(true)

	// Read server INFO
	line, err := client.reader.ReadString('\n')
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("failed to read INFO: %w", err)
	}

	// Parse INFO to check if auth is required
	requiresAuth := strings.Contains(line, `"auth_required":true`)

	// Send CONNECT message
	connectMsg := map[string]interface{}{
		"verbose":  false,
		"pedantic": false,
		"name":     "traefik-http-to-nats",
	}

	if requiresAuth {
		if token != "" {
			connectMsg["auth_token"] = token
		} else if username != "" && password != "" {
			connectMsg["user"] = username
			connectMsg["pass"] = password
		}
	}

	connectJSON, _ := json.Marshal(connectMsg)
	_, err = client.writer.WriteString(fmt.Sprintf("CONNECT %s\r\n", connectJSON))
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("failed to send CONNECT: %w", err)
	}

	// Send PING
	_, err = client.writer.WriteString("PING\r\n")
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("failed to send PING: %w", err)
	}

	err = client.writer.Flush()
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("failed to flush: %w", err)
	}

	// Wait for PONG
	pong, err := client.reader.ReadString('\n')
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("failed to read PONG: %w", err)
	}

	if !strings.HasPrefix(pong, "PONG") {
		conn.Close()
		return nil, fmt.Errorf("expected PONG, got: %s", pong)
	}

	// Start message reader goroutine
	go client.readLoop()

	return client, nil
}

// readLoop continuously reads messages from NATS
func (c *NatsClient) readLoop() {
	fmt.Println("[NATS] Starting readLoop")
	defer func() {
		c.connected.Store(false)
		fmt.Println("[NATS] Exiting readLoop")
	}()

	for {
		select {
		case <-c.closeChan:
			return
		default:
		}

		line, err := c.reader.ReadString('\n')
		if err != nil {
			if c.connected.Load() {
				fmt.Printf("[NATS] readLoop error while connected: %v\n", err)
				c.connected.Store(false)
			}
			return
		}

		line = strings.TrimSpace(line)
		fmt.Println(line)

		if strings.HasPrefix(line, "MSG ") {
			fmt.Printf("[NATS] Received MSG: %s\n", line)
			// Parse: MSG <subject> <sid> [reply-to] <#bytes>
			parts := strings.Fields(line)
			if len(parts) < 4 {
				fmt.Printf("[NATS] Invalid MSG format, parts: %d\n", len(parts))
				continue
			}

			subject := parts[1]
			var bytesStr string

			if len(parts) == 4 {
				// No reply-to
				bytesStr = parts[3]
			} else {
				replyTo := parts[3]
				bytesStr = parts[4]

				// Has reply-to
				fmt.Printf("[NATS] MSG with reply-to: %s, bytes: %s\n", replyTo, bytesStr)
				continue // not a reply message, rather it's request of something else
			}

			// Read message payload
			var numBytes int
			fmt.Sscanf(bytesStr, "%d", &numBytes)

			payload := make([]byte, numBytes+2) // +2 for \r\n
			_, err = io.ReadFull(c.reader, payload)
			if err != nil {
				fmt.Printf("[NATS] Failed to read payload: %v\n", err)
				continue
			}

			// Trim \r\n
			payload = payload[:numBytes]
			fmt.Printf("[NATS] Received payload (%d bytes): %s\n", numBytes, string(payload))

			// Deliver to subscription based on subject (which is the inbox for responses)
			c.mu.Lock()
			if ch, exists := c.subscriptions[subject]; exists {
				fmt.Printf("[NATS] Delivering to subscription: %s\n", subject)
				select {
				case ch <- payload:
					fmt.Printf("[NATS] Delivered to channel for: %s\n", subject)
				default:
					fmt.Printf("[NATS] Channel full for: %s\n", subject)
				}
			} else {
				fmt.Printf("[NATS] No subscription found for subject: %s\n", subject)
			}
			c.mu.Unlock()
		} else if strings.HasPrefix(line, "PING") {
			fmt.Println("[NATS] Received PING, sending PONG")
			c.writer.WriteString("PONG\r\n")
			c.writer.Flush()
		}
	}
}

// Request sends a request to NATS and waits for a response
func (c *NatsClient) Request(subject string, data []byte, timeout time.Duration) ([]byte, error) {
	if !c.connected.Load() {
		return nil, fmt.Errorf("not connected to NATS")
	}

	// Generate UUID for inbox
	uuid, errUuid := generateUUID()
	if errUuid != nil {
		return nil, fmt.Errorf("failed to generate UUID: %w", errUuid)
	}
	inbox := fmt.Sprintf("%s%s", c.inboxPrefix, uuid)

	// Generate unique subscription ID
	c.mu.Lock()
	c.sidCounter++
	sid := c.sidCounter
	c.mu.Unlock()

	c.mu.Lock()
	// Create response channel
	respCh := make(chan []byte, 1)
	c.subscriptions[inbox] = respCh
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		delete(c.subscriptions, inbox)
		close(respCh)
		c.mu.Unlock()
	}()

	// Subscribe to inbox
	c.mu.Lock()
	_, err := c.writer.WriteString(fmt.Sprintf("SUB %s %d\r\n", inbox, sid))
	if err != nil {
		c.mu.Unlock()
		return nil, fmt.Errorf("failed to subscribe: %w", err)
	}

	// Publish message
	_, err = c.writer.WriteString(fmt.Sprintf("PUB %s %s %d\r\n", subject, inbox, len(data)))
	if err != nil {
		c.mu.Unlock()
		return nil, fmt.Errorf("failed to publish: %w", err)
	}

	_, err = c.writer.Write(data)
	if err != nil {
		c.mu.Unlock()
		return nil, fmt.Errorf("failed to write data: %w", err)
	}

	_, err = c.writer.WriteString("\r\n")
	if err != nil {
		c.mu.Unlock()
		return nil, fmt.Errorf("failed to write CRLF: %w", err)
	}

	err = c.writer.Flush()
	c.mu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("failed to flush: %w", err)
	}

	// Wait for response or timeout
	select {
	case resp := <-respCh:
		// Unsubscribe
		c.mu.Lock()
		c.writer.WriteString(fmt.Sprintf("UNSUB %d\r\n", sid))
		c.writer.Flush()
		c.mu.Unlock()
		return resp, nil
	case <-time.After(timeout):
		// Unsubscribe on timeout
		c.mu.Lock()
		c.writer.WriteString(fmt.Sprintf("UNSUB %d\r\n", sid))
		c.writer.Flush()
		c.mu.Unlock()
		return nil, fmt.Errorf("timeout")
	}
}

// generateUUID generates a UUID v4 string
func generateUUID() (string, error) {
	uuid := make([]byte, 16)
	if _, err := rand.Read(uuid); err != nil {
		return "", err
	}

	// Set version (4) and variant bits according to RFC 4122
	uuid[6] = (uuid[6] & 0x0f) | 0x40 // Version 4
	uuid[8] = (uuid[8] & 0x3f) | 0x80 // Variant is 10

	// Format as hex string (we don't need dashes for NATS inbox)
	return hex.EncodeToString(uuid), nil
}

// IsConnected returns whether the client is currently connected
func (c *NatsClient) IsConnected() bool {
	return c.connected.Load()
}

// Close closes the NATS connection
func (c *NatsClient) Close() error {
	if !c.connected.Load() {
		return nil
	}

	c.connected.Store(false)
	close(c.closeChan)

	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}
