# Quick Start Guide

This guide will help you get the Traefik HTTP to NATS plugin up and running in minutes.

## Prerequisites

- Docker and Docker Compose installed
- Basic understanding of Traefik and NATS

## Step 1: Clone the Repository

```bash
git clone https://github.com/insoftcz/traefik-plugin-http-to-nats.git
cd traefik-plugin-http-to-nats
```

## Step 2: Start the Example Stack

The example includes NATS server, a sample NATS service, and Traefik with the plugin configured:

```bash
cd examples
docker-compose up -d
```

This will start:
- **NATS Server** on port 4222 (client) and 8222 (monitoring)
- **NATS Service** (example backend that responds to requests)
- **Traefik** on port 80 (HTTP) and 8080 (dashboard)

## Step 3: Verify Services are Running

```bash
# Check all services are up
docker-compose ps

# Check Traefik logs
docker-compose logs traefik

# Check NATS service logs
docker-compose logs nats-service
```

## Step 4: Test the Plugin

### Test 1: Simple GET Request

```bash
curl http://localhost/api/users
```

Expected response:
```json
{"users": [{"id": 1, "name": "John Doe"}, {"id": 2, "name": "Jane Smith"}]}
```

### Test 2: POST Request with JSON Body

```bash
curl -X POST http://localhost/api/users \
  -H "Content-Type: application/json" \
  -d '{"name": "John Doe", "email": "john@example.com"}'
```

Expected response:
```json
{"message": "User created", "received": {"name":"John Doe","email":"john@example.com"}}
```

### Test 3: Different Subject (Orders)

```bash
curl http://localhost/api/orders
```

Expected response:
```json
{"orders": [{"id": 101, "total": 99.99}, {"id": 102, "total": 149.99}]}
```

### Test 4: Run Full Test Suite

```bash
chmod +x test-requests.sh
./test-requests.sh
```

## Step 5: Monitor Traffic

### Traefik Dashboard
Open in your browser: http://localhost:8080/dashboard/

### NATS Monitoring
Open in your browser: http://localhost:8222/

### Watch NATS Messages in Real-Time

```bash
docker exec -it nats-box nats sub '>' --translate
```

Then in another terminal, send requests:

```bash
curl http://localhost/api/users
```

You'll see the NATS messages flowing through in real-time!

## Understanding the Flow

1. **HTTP Request** → `http://localhost/api/users`
2. **Traefik** matches the route `/api/*`
3. **Plugin** extracts `users` from the URL
4. **NATS Message** published to subject `users`
5. **NATS Service** responds with data
6. **Plugin** converts NATS response to HTTP
7. **HTTP Response** returned to client

## Configuration

The plugin is configured in `traefik-config/dynamic-config.yml`:

```yaml
http:
  middlewares:
    nats-gateway:
      plugin:
        http-to-nats:
          natsUrl: "nats://nats:4222"
          subjectPattern: "/api/{subject}"
          timeout: 5000

  routers:
    api-router:
      rule: "PathPrefix(`/api/`)"
      service: noop@internal
      middlewares:
        - nats-gateway
```

### Key Configuration Options

- **natsUrl**: NATS server address (default: `nats://localhost:4222`)
- **subjectPattern**: URL pattern with `{subject}` placeholder
- **timeout**: Request timeout in milliseconds (default: 5000)
- **username/password**: NATS authentication (optional)
- **token**: NATS token authentication (optional)

## Customizing the NATS Service

Edit `nats-service/main.go` to add your own business logic:

```go
func handleUsers(req Request) []interface{} {
    // Your custom logic here
    // Return tuple: [statusCode, data]
    // statusCode 1 = success, any other value = error
    return []interface{}{
        1, // Success status
        map[string]interface{}{
            "custom": "response",
        },
    }
}
```

Rebuild and restart:

```bash
docker-compose up -d --build nats-service
```

## Using in Your Own Project

### Option 1: Local Plugin (Development)

```yaml
# traefik.yml
experimental:
  localPlugins:
    http-to-nats:
      moduleName: github.com/insoftcz/traefik-plugin-http-to-nats
```

Mount the plugin directory:
```yaml
volumes:
  - /path/to/traefik-plugin-http-to-nats:/plugins-local/src/github.com/insoftcz/traefik-plugin-http-to-nats
```

### Option 2: Plugin Catalog (Production)

```yaml
# traefik.yml
experimental:
  plugins:
    http-to-nats:
      moduleName: github.com/insoftcz/traefik-plugin-http-to-nats
      version: v1.0.0
```

## Troubleshooting

### Plugin Not Loading

Check Traefik logs:
```bash
docker-compose logs traefik | grep -i error
```

Common issues:
- Module name mismatch in `.traefik.yml` and `go.mod`
- Plugin directory not properly mounted
- Syntax error in dynamic configuration

### NATS Connection Failed

Check NATS is running:
```bash
docker-compose logs nats
curl http://localhost:8222/varz
```

Verify network connectivity:
```bash
docker exec -it traefik ping nats
```

### No Response from NATS Service

Check NATS service is subscribed:
```bash
docker-compose logs nats-service
```

Test NATS directly:
```bash
docker exec -it nats-box nats pub users '{"test":"data"}'
```

### Timeout Errors

Increase timeout in configuration:
```yaml
timeout: 10000  # 10 seconds
```

## Next Steps

- Read the full [README.md](../README.md) for detailed documentation
- Explore different URL patterns and routing strategies
- Implement your own NATS microservices
- Add authentication and security
- Scale your NATS services with JetStream

## Clean Up

To stop and remove all containers:

```bash
docker-compose down
```

To remove volumes as well:

```bash
docker-compose down -v
```

## Support

- GitHub Issues: https://github.com/insoftcz/traefik-plugin-http-to-nats/issues
- NATS Documentation: https://docs.nats.io/
- Traefik Plugin Guide: https://doc.traefik.io/traefik/plugins/

## Example Use Cases

- **Microservices Gateway**: Route HTTP traffic to NATS microservices
- **Event-Driven APIs**: Convert REST APIs to event-driven architecture
- **Service Mesh**: Integrate with NATS-based service mesh
- **Async Processing**: Queue requests for async processing
- **Fan-out/Fan-in**: Distribute requests across multiple services