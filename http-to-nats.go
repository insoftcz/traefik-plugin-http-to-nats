package traefik_plugin_http_to_nats

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"time"

	"github.com/nats-io/nats.go"
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
		SubjectPattern: "/nats/{subject}",
		Timeout:        5000, // milliseconds
	}
}

// HttpToNats holds the plugin instance.
type HttpToNats struct {
	next           http.Handler
	name           string
	natsConn       *nats.Conn
	subjectPattern string
	pathRegex      *regexp.Regexp
	timeout        time.Duration
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

	// Set up NATS connection options
	opts := []nats.Option{
		nats.Name("Traefik HTTP-to-NATS Plugin"),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(2 * time.Second),
	}

	// Add authentication if provided
	if config.Username != "" && config.Password != "" {
		opts = append(opts, nats.UserInfo(config.Username, config.Password))
	} else if config.Token != "" {
		opts = append(opts, nats.Token(config.Token))
	}

	// Connect to NATS
	nc, err := nats.Connect(config.NatsUrl, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to NATS: %w", err)
	}

	timeout := time.Duration(config.Timeout) * time.Millisecond
	if timeout == 0 {
		timeout = 5 * time.Second
	}

	return &HttpToNats{
		next:           next,
		name:           name,
		natsConn:       nc,
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

// ServeHTTP handles the HTTP request and forwards it to NATS.
func (h *HttpToNats) ServeHTTP(rw http.ResponseWriter, req *http.Request) {
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

	// Build headers map
	headers := make(map[string]string)
	for key, values := range req.Header {
		if len(values) > 0 {
			headers[key] = values[0]
		}
	}

	// Build query params map
	query := make(map[string]string)
	for key, values := range req.URL.Query() {
		if len(values) > 0 {
			query[key] = values[0]
		}
	}

	// Create NATS request
	natsReq := NatsRequest{
		Method:  req.Method,
		Path:    req.URL.Path,
		Headers: headers,
		Query:   query,
		Body:    string(bodyBytes),
	}

	// Marshal to JSON
	reqJSON, err := json.Marshal(natsReq)
	if err != nil {
		http.Error(rw, "Failed to marshal request", http.StatusInternalServerError)
		return
	}

	// Send request to NATS and wait for response
	msg, err := h.natsConn.Request(subject, reqJSON, h.timeout)
	if err != nil {
		if err == nats.ErrTimeout {
			http.Error(rw, "NATS request timeout", http.StatusGatewayTimeout)
		} else {
			http.Error(rw, fmt.Sprintf("NATS request failed: %v", err), http.StatusBadGateway)
		}
		return
	}

	// Parse NATS response
	var natsResp NatsResponse
	if err := json.Unmarshal(msg.Data, &natsResp); err != nil {
		http.Error(rw, "Failed to parse NATS response", http.StatusBadGateway)
		return
	}

	// Write response headers
	if natsResp.Headers != nil {
		for key, value := range natsResp.Headers {
			rw.Header().Set(key, value)
		}
	}

	// Write status code
	statusCode := natsResp.StatusCode
	if statusCode == 0 {
		statusCode = http.StatusOK
	}
	rw.WriteHeader(statusCode)

	// Write response body
	if natsResp.Body != "" {
		rw.Write([]byte(natsResp.Body))
	}
}
