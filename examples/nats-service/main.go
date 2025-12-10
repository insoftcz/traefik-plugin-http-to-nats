package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/nats-io/nats.go"
)

// Request represents the incoming request from the Traefik plugin
type Request struct {
	Method  string            `json:"method"`
	Path    string            `json:"path"`
	Headers map[string]string `json:"headers"`
	Query   map[string]string `json:"query"`
	Body    string            `json:"body,omitempty"`
}

// Response represents the response to send back to the Traefik plugin
type Response struct {
	StatusCode int               `json:"statusCode"`
	Headers    map[string]string `json:"headers,omitempty"`
	Body       string            `json:"body,omitempty"`
}

func main() {
	// Get NATS URL from environment or use default
	natsURL := os.Getenv("NATS_URL")
	if natsURL == "" {
		natsURL = nats.DefaultURL
	}

	// Connect to NATS
	nc, err := nats.Connect(natsURL,
		nats.Name("Example NATS Service"),
		nats.ReconnectWait(2*time.Second),
		nats.MaxReconnects(-1),
	)
	if err != nil {
		log.Fatalf("Failed to connect to NATS: %v", err)
	}
	defer nc.Close()

	log.Printf("Connected to NATS at %s", natsURL)

	// Subscribe to multiple subjects
	subjects := []string{"users", "orders", "products"}

	for _, subject := range subjects {
		_, err := nc.Subscribe(subject, createHandler(subject))
		if err != nil {
			log.Fatalf("Failed to subscribe to %s: %v", subject, err)
		}
		log.Printf("Subscribed to subject: %s", subject)
	}

	// Wait for interrupt signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	log.Println("NATS service is running. Press Ctrl+C to exit.")
	<-sigChan

	log.Println("Shutting down...")
}

// createHandler creates a message handler for a specific subject
func createHandler(subject string) nats.MsgHandler {
	return func(m *nats.Msg) {
		log.Printf("Received message on subject '%s'", subject)

		// Parse the incoming request
		var req Request
		if err := json.Unmarshal(m.Data, &req); err != nil {
			log.Printf("Error parsing request: %v", err)
			sendErrorResponse(m, 400, "Invalid request format")
			return
		}

		log.Printf("Method: %s, Path: %s", req.Method, req.Path)

		// Process the request based on subject and method
		var resp Response
		switch subject {
		case "users":
			resp = handleUsers(req)
		case "orders":
			resp = handleOrders(req)
		case "products":
			resp = handleProducts(req)
		default:
			resp = Response{
				StatusCode: 404,
				Headers: map[string]string{
					"Content-Type": "application/json",
				},
				Body: fmt.Sprintf(`{"error": "Unknown subject: %s"}`, subject),
			}
		}

		// Send the response
		data, err := json.Marshal(resp)
		if err != nil {
			log.Printf("Error marshaling response: %v", err)
			sendErrorResponse(m, 500, "Internal server error")
			return
		}

		if err := m.Respond(data); err != nil {
			log.Printf("Error sending response: %v", err)
		}
	}
}

// handleUsers processes requests for the "users" subject
func handleUsers(req Request) Response {
	switch req.Method {
	case "GET":
		return Response{
			StatusCode: 200,
			Headers: map[string]string{
				"Content-Type": "application/json",
			},
			Body: `{"users": [{"id": 1, "name": "John Doe"}, {"id": 2, "name": "Jane Smith"}]}`,
		}
	case "POST":
		return Response{
			StatusCode: 201,
			Headers: map[string]string{
				"Content-Type": "application/json",
			},
			Body: fmt.Sprintf(`{"message": "User created", "received": %s}`, req.Body),
		}
	case "PUT":
		return Response{
			StatusCode: 200,
			Headers: map[string]string{
				"Content-Type": "application/json",
			},
			Body: `{"message": "User updated"}`,
		}
	case "DELETE":
		return Response{
			StatusCode: 204,
			Headers: map[string]string{
				"Content-Type": "application/json",
			},
		}
	default:
		return Response{
			StatusCode: 405,
			Headers: map[string]string{
				"Content-Type": "application/json",
			},
			Body: `{"error": "Method not allowed"}`,
		}
	}
}

// handleOrders processes requests for the "orders" subject
func handleOrders(req Request) Response {
	switch req.Method {
	case "GET":
		return Response{
			StatusCode: 200,
			Headers: map[string]string{
				"Content-Type": "application/json",
			},
			Body: `{"orders": [{"id": 101, "total": 99.99}, {"id": 102, "total": 149.99}]}`,
		}
	case "POST":
		return Response{
			StatusCode: 201,
			Headers: map[string]string{
				"Content-Type": "application/json",
			},
			Body: `{"message": "Order created", "orderId": 103}`,
		}
	default:
		return Response{
			StatusCode: 405,
			Headers: map[string]string{
				"Content-Type": "application/json",
			},
			Body: `{"error": "Method not allowed"}`,
		}
	}
}

// handleProducts processes requests for the "products" subject
func handleProducts(req Request) Response {
	return Response{
		StatusCode: 200,
		Headers: map[string]string{
			"Content-Type": "application/json",
		},
		Body: `{"products": [{"id": 1, "name": "Widget", "price": 19.99}]}`,
	}
}

// sendErrorResponse sends an error response
func sendErrorResponse(m *nats.Msg, statusCode int, message string) {
	resp := Response{
		StatusCode: statusCode,
		Headers: map[string]string{
			"Content-Type": "application/json",
		},
		Body: fmt.Sprintf(`{"error": "%s"}`, message),
	}

	data, _ := json.Marshal(resp)
	m.Respond(data)
}
