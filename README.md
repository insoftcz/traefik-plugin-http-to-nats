# Traefik HTTP to NATS Plugin

A Traefik middleware plugin that forwards HTTP requests to NATS based on URL patterns. This plugin extracts dynamic parts from the URL path and uses them as NATS subjects, enabling seamless HTTP-to-NATS request routing.

**Note:** This plugin uses only Go's standard library and implements a lightweight NATS protocol client, making it compatible with Traefik's Yaegi interpreter without external dependencies.

## Features

- 🚀 Dynamic URL pattern matching with named placeholders
- 📨 Forwards HTTP requests to NATS subjects
- 🔄 Request-reply pattern with configurable timeout
- 🔐 Supports NATS authentication (username/password or token)
- 📦 Preserves HTTP method, headers, query parameters, and body
- ⚡ Automatic reconnection to NATS server

## How It Works

1. The plugin matches incoming HTTP requests against a configured URL pattern
2. Extracts the dynamic part(s) from the URL to use as the NATS subject
3. Converts the HTTP request to JSON format
4. Publishes the request to NATS and waits for a response
5. Converts the NATS response back to an HTTP response

## Configuration

### Plugin Configuration Options

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `natsUrl` | string | Yes | `nats://localhost:4222` | NATS server URL |
| `subjectPattern` | string | Yes | `/api/{subject}` | URL pattern with placeholders |
| `timeout` | int | No | `5000` | Request timeout in milliseconds |
| `username` | string | No | - | NATS authentication username |
| `password` | string | No | - | NATS authentication password |
| `token` | string | No | - | NATS authentication token |

### Subject Pattern Syntax

The `subjectPattern` uses placeholders enclosed in curly braces `{}` to define dynamic parts of the URL:

- `/api/{subject}` - Matches `/api/users`, `/api/orders`, etc.
- `/v1/{service}/{action}` - Matches `/v1/users/create`, `/v1/orders/list`, etc.

The placeholder named `subject` will be used as the NATS subject. If you use a different name, the first captured group will be used.

## Installation

### Local Mode (for development)

Add the plugin to your Traefik static configuration:

```yaml
# traefik.yml
experimental:
  localPlugins:
    http-to-nats:
      moduleName: github.com/insoftcz/traefik-plugin-http-to-nats
```

### Plugin Catalog (for production)

```yaml
# traefik.yml
experimental:
  plugins:
    http-to-nats:
      moduleName: github.com/insoftcz/traefik-plugin-http-to-nats
      version: v1.0.0
```

## Usage Examples

### Example 1: Simple API Gateway

**Traefik Dynamic Configuration:**

```yaml
# dynamic-config.yml
http:
  middlewares:
    nats-gateway:
      plugin:
        http-to-nats:
          natsUrl: "nats://localhost:4222"
          subjectPattern: "/api/{subject}"
          timeout: 3000

  routers:
    api-router:
      rule: "PathPrefix(`/api/`)"
      service: noop@internal
      middlewares:
        - nats-gateway
```

**Request:**
```bash
curl -X POST http://localhost:80/api/users \
  -H "Content-Type: application/json" \
  -d '{"name": "John Doe"}'
```

**NATS Subject:** `users`

### Example 2: Multi-segment Pattern

```yaml
http:
  middlewares:
    nats-microservices:
      plugin:
        http-to-nats:
          natsUrl: "nats://nats-server:4222"
          subjectPattern: "/v1/{subject}/action"
          timeout: 5000
          username: "traefik"
          password: "secret"
```

**Request:**
```bash
curl http://localhost:80/v1/orders/action
```

**NATS Subject:** `orders`

### Example 3: With Authentication

```yaml
http:
  middlewares:
    nats-secure:
      plugin:
        http-to-nats:
          natsUrl: "nats://secure-nats:4222"
          subjectPattern: "/api/{subject}"
          token: "my-secret-token"
          timeout: 10000
```

## Request Format

The plugin sends the following JSON structure to NATS:

```json
{
  "method": "POST",
  "path": "/api/users",
  "headers": {
    "Content-Type": "application/json",
    "Authorization": "Bearer token123"
  },
  "query": {
    "filter": "active"
  },
  "body": "{\"name\": \"John Doe\"}"
}
```

## Response Format

Your NATS service should respond with a JSON array (tuple) containing a status code and data:

```json
[1, {"id": 123, "name": "John Doe"}]
```

**Format:** `[statusCode, data]`

- **`statusCode`**: Use `1` to indicate success. Any other value is treated as an error.
- **`data`**: The response payload. Can be a string, object, array, or null.

**Examples:**

Success with JSON object:
```json
[1, {"id": 123, "name": "John Doe"}]
```

Success with string:
```json
[1, "Operation completed successfully"]
```

Success with no body:
```json
[1, null]
```

Error response (statusCode != 1):
```json
[0, "Database connection failed"]
```

## NATS Service Example

Here's a simple Go example of a NATS service that responds to the plugin:

```go
package main

import (
    "encoding/json"
    "log"
    "github.com/nats-io/nats.go"
)

type Request struct {
    Method  string            `json:"method"`
    Path    string            `json:"path"`
    Headers map[string]string `json:"headers"`
    Query   map[string]string `json:"query"`
    Body    string            `json:"body"`
}

func main() {
    nc, _ := nats.Connect(nats.DefaultURL)
    defer nc.Close()

    nc.Subscribe("users", func(m *nats.Msg) {
        var req Request
        json.Unmarshal(m.Data, &req)

        // Respond with tuple: [statusCode, data]
        // statusCode 1 = success, any other value = error
        resp := []interface{}{
            1, // Success status
            map[string]interface{}{
                "message": "User created successfully",
                "method": req.Method,
            },
        }

        data, _ := json.Marshal(resp)
        m.Respond(data)
    })

    log.Println("Listening on subject: users")
    select {}
}
```

## Docker Compose Example

```yaml
version: '3.8'

services:
  nats:
    image: nats:latest
    ports:
      - "4222:4222"
      - "8222:8222"
    command: "-js -m 8222"

  traefik:
    image: traefik:v3.0
    ports:
      - "80:80"
      - "8080:8080"
    volumes:
      - ./traefik.yml:/etc/traefik/traefik.yml
      - ./dynamic-config.yml:/etc/traefik/dynamic-config.yml
      - ./plugins-local:/plugins-local
    depends_on:
      - nats
```

## Error Handling

The plugin returns appropriate HTTP status codes:

- `400 Bad Request` - Invalid URL path (doesn't match pattern)
- `502 Bad Gateway` - NATS connection error or invalid response
- `504 Gateway Timeout` - NATS request timeout

## Development

### Building

```bash
go mod download
go build
```

### Testing

```bash
go test -v ./...
```

### Running Locally

1. Start a NATS server:
```bash
docker run -p 4222:4222 nats:latest
```

2. Configure Traefik with the plugin
3. Send test requests

## License

MIT License

## Contributing

Contributions are welcome! Please feel free to submit a Pull Request.

## Support

For issues and questions, please open an issue on GitHub.
