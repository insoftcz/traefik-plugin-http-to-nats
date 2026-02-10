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

// ensureConnected ensures the NATS client is connected (lazy initialization)
func (h *HttpToNats) ensureConnected() error {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.natsClient != nil {
		return nil
	}

	// Create NATS client connection
	client, err := NewNatsClient(h.natsConfig.host, h.natsConfig.username, h.natsConfig.password, h.natsConfig.token)
	if err != nil {
		return fmt.Errorf("failed to connect to NATS: %w", err)
	}

	h.natsClient = client
	return nil
}

// ServeHTTP handles the HTTP request and forwards it to NATS.
func (h *HttpToNats) ServeHTTP(rw http.ResponseWriter, req *http.Request) {
	// Ensure NATS connection is established (lazy initialization)
	if err := h.ensureConnected(); err != nil {
		http.Error(rw, fmt.Sprintf("Failed to connect to NATS: %v", err), http.StatusServiceUnavailable)
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
		if err.Error() == "timeout" {
			http.Error(rw, "NATS request timeout", http.StatusGatewayTimeout)
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

	// // Parse NATS response
	// var natsResp NatsResponse
	// fmt.Printf("[NATS] Received payload %s\n", string(respData))

	// if err := json.Unmarshal(respData, &natsResp); err != nil {
	// 	fmt.Println(err.Error())
	// 	http.Error(rw, "Failed to parse NATS response", http.StatusBadGateway)
	// 	return
	// }

	// // Write response headers
	// if natsResp.Headers != nil {
	// 	for key, value := range natsResp.Headers {
	// 		rw.Header().Set(key, value)
	// 	}
	// }

	// // Write status code
	// statusCode := natsResp.StatusCode
	// if statusCode == 0 {
	// 	statusCode = http.StatusOK
	// }
	// rw.WriteHeader(statusCode)

	// // Write response body
	// if natsResp.Body != "" {
	// 	rw.Write([]byte(natsResp.Body))
	// }
}

// NatsClient implements a basic NATS client using the NATS protocol
type NatsClient struct {
	conn          net.Conn
	reader        *bufio.Reader
	writer        *bufio.Writer
	mu            sync.Mutex
	subscriptions map[string]chan []byte
	inboxPrefix   string
	connected     bool
	sidCounter    uint64
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
		connected:     true,
		sidCounter:    0,
	}

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
	for c.connected {
		line, err := c.reader.ReadString('\n')
		if err != nil {
			if c.connected {
				fmt.Printf("[NATS] readLoop error while connected: %v\n", err)
				c.connected = false
			}
			fmt.Println("[NATS] Exiting readLoop")
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
				continue; // not a reply message, rather it's request of something else
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
	if !c.connected {
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

// Close closes the NATS connection
func (c *NatsClient) Close() error {
	c.connected = false
	return c.conn.Close()
}
