#!/bin/bash

# Test script for Traefik HTTP to NATS Plugin
# This script sends various HTTP requests to test the plugin functionality

set -e

# Configuration
TRAEFIK_URL="${TRAEFIK_URL:-http://localhost:80}"
VERBOSE="${VERBOSE:-false}"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Function to print colored output
print_test() {
    echo -e "${BLUE}=== $1 ===${NC}"
}

print_success() {
    echo -e "${GREEN}✓ $1${NC}"
}

print_error() {
    echo -e "${RED}✗ $1${NC}"
}

print_info() {
    echo -e "${YELLOW}ℹ $1${NC}"
}

# Function to make HTTP request and show result
make_request() {
    local method=$1
    local path=$2
    local data=$3
    local description=$4

    print_test "$description"

    if [ "$VERBOSE" = "true" ]; then
        echo "Request: $method $TRAEFIK_URL$path"
        if [ -n "$data" ]; then
            echo "Data: $data"
        fi
    fi

    local response
    local http_code

    if [ -n "$data" ]; then
        response=$(curl -s -w "\n%{http_code}" -X "$method" "$TRAEFIK_URL$path" \
            -H "Content-Type: application/json" \
            -d "$data")
    else
        response=$(curl -s -w "\n%{http_code}" -X "$method" "$TRAEFIK_URL$path")
    fi

    http_code=$(echo "$response" | tail -n 1)
    body=$(echo "$response" | sed '$d')

    echo "Status Code: $http_code"
    echo "Response Body:"
    echo "$body" | jq '.' 2>/dev/null || echo "$body"

    if [ "$http_code" -ge 200 ] && [ "$http_code" -lt 300 ]; then
        print_success "Request successful"
    elif [ "$http_code" -ge 400 ] && [ "$http_code" -lt 500 ]; then
        print_error "Client error"
    elif [ "$http_code" -ge 500 ]; then
        print_error "Server error"
    fi

    echo ""
}

# Function to check if services are running
check_services() {
    print_info "Checking if services are available..."

    # Check Traefik
    if curl -s -f "$TRAEFIK_URL" > /dev/null 2>&1 || curl -s "http://localhost:8080/api/overview" > /dev/null 2>&1; then
        print_success "Traefik is running"
    else
        print_error "Traefik is not accessible at $TRAEFIK_URL"
        print_info "Make sure Traefik is running with: docker-compose up -d"
        exit 1
    fi

    echo ""
}

# Main test suite
main() {
    echo -e "${BLUE}"
    echo "╔═══════════════════════════════════════════════════════════╗"
    echo "║   Traefik HTTP to NATS Plugin - Test Suite               ║"
    echo "╚═══════════════════════════════════════════════════════════╝"
    echo -e "${NC}"

    check_services

    # Test 1: GET request to /api/users
    make_request "GET" "/api/users" "" \
        "Test 1: GET /api/users (subject: users)"

    # Test 2: POST request to /api/users
    make_request "POST" "/api/users" '{"name":"John Doe","email":"john@example.com"}' \
        "Test 2: POST /api/users with JSON body"

    # Test 3: GET request to /api/orders
    make_request "GET" "/api/orders" "" \
        "Test 3: GET /api/orders (subject: orders)"

    # Test 4: POST request to /api/orders
    make_request "POST" "/api/orders" '{"product":"Widget","quantity":5,"total":99.99}' \
        "Test 4: POST /api/orders with order data"

    # Test 5: GET request to /api/products
    make_request "GET" "/api/products" "" \
        "Test 5: GET /api/products (subject: products)"

    # Test 6: PUT request to /api/users
    make_request "PUT" "/api/users" '{"id":1,"name":"Jane Smith"}' \
        "Test 6: PUT /api/users to update user"

    # Test 7: DELETE request to /api/users
    make_request "DELETE" "/api/users" "" \
        "Test 7: DELETE /api/users"

    # Test 8: Invalid path (should fail)
    print_test "Test 8: Invalid path /invalid/path (should return 400)"
    response=$(curl -s -w "\n%{http_code}" "$TRAEFIK_URL/invalid/path")
    http_code=$(echo "$response" | tail -n 1)
    echo "Status Code: $http_code"
    if [ "$http_code" = "404" ] || [ "$http_code" = "400" ]; then
        print_success "Correctly rejected invalid path"
    else
        print_error "Unexpected status code for invalid path"
    fi
    echo ""

    # Test 9: GET with query parameters
    print_test "Test 9: GET /api/users with query parameters"
    response=$(curl -s -w "\n%{http_code}" "$TRAEFIK_URL/api/users?filter=active&limit=10")
    http_code=$(echo "$response" | tail -n 1)
    body=$(echo "$response" | sed '$d')
    echo "Status Code: $http_code"
    echo "Response Body:"
    echo "$body" | jq '.' 2>/dev/null || echo "$body"
    if [ "$http_code" -ge 200 ] && [ "$http_code" -lt 300 ]; then
        print_success "Query parameters handled successfully"
    fi
    echo ""

    # Test 10: Request with custom headers
    print_test "Test 10: GET /api/users with custom headers"
    response=$(curl -s -w "\n%{http_code}" "$TRAEFIK_URL/api/users" \
        -H "X-Request-ID: test-123" \
        -H "Authorization: Bearer test-token")
    http_code=$(echo "$response" | tail -n 1)
    body=$(echo "$response" | sed '$d')
    echo "Status Code: $http_code"
    echo "Response Body:"
    echo "$body" | jq '.' 2>/dev/null || echo "$body"
    if [ "$http_code" -ge 200 ] && [ "$http_code" -lt 300 ]; then
        print_success "Custom headers handled successfully"
    fi
    echo ""

    # Summary
    echo -e "${BLUE}"
    echo "╔═══════════════════════════════════════════════════════════╗"
    echo "║   Test Suite Complete                                     ║"
    echo "╚═══════════════════════════════════════════════════════════╝"
    echo -e "${NC}"

    print_info "Dashboard: http://localhost:8080/dashboard/"
    print_info "NATS Monitoring: http://localhost:8222/"
    print_info ""
    print_info "To test with different routes, try:"
    echo "  curl $TRAEFIK_URL/api/users"
    echo "  curl $TRAEFIK_URL/api/orders"
    echo "  curl $TRAEFIK_URL/api/products"
    echo ""
    print_info "To see NATS traffic in real-time, use nats-box:"
    echo "  docker exec -it nats-box nats sub '>' --translate"
}

# Run main function
main
