#!/bin/bash
# Test proxy functionality for a container

set -e

if [ $# -eq 0 ]; then
    echo "Usage: $0 <machine-name>"
    echo "Example: $0 able-yankee"
    echo "This script tests HTTP proxy functionality for the specified container."
    exit 1
fi

MACHINE_NAME="$1"
PROXY_URL="http://${MACHINE_NAME}.exe.cloud:8080"

echo "Testing HTTP proxy functionality for container: $MACHINE_NAME"

# Function to test HTTP response
test_http_response() {
    local url="$1"
    local expected_pattern="$2"
    local description="$3"

    echo "\n=== Testing: $description ==="
    echo "URL: $url"

    # Test with curl and capture both response and HTTP status
    response=$(curl -s -w "HTTPSTATUS:%{http_code}" "$url" 2>/dev/null || echo "FAILED")

    if [[ "$response" == "FAILED" ]]; then
        echo "❌ Request failed"
        return 1
    fi

    # Extract HTTP status and body
    http_status=$(echo "$response" | tr -d '\n' | sed -e 's/.*HTTPSTATUS://')
    body=$(echo "$response" | sed -e 's/HTTPSTATUS:.*//')

    echo "HTTP Status: $http_status"

    if [[ "$body" =~ $expected_pattern ]]; then
        echo "✅ Response contains expected pattern: $expected_pattern"
        return 0
    else
        echo "❌ Response does not match expected pattern: $expected_pattern"
        echo "Actual response: $body" | head -5
        return 1
    fi
}

# Function to check route configuration
check_route() {
    local expected_port="$1"
    local expected_share="$2"
    local description="$3"

    echo "\n=== Testing: $description ==="

    route_output=$(ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -p 2222 localhost "route $MACHINE_NAME" 2>/dev/null)

    echo "Route configuration: $route_output"

    if [[ "$route_output" =~ port.*$expected_port ]] && [[ "$route_output" =~ share.*$expected_share ]]; then
        echo "✅ Route configuration matches expectations (port: $expected_port, share: $expected_share)"
        return 0
    else
        echo "❌ Route configuration doesn't match expectations"
        echo "Expected: port $expected_port, share $expected_share"
        return 1
    fi
}

# Function to start HTTP server in container
start_http_server() {
    local port="$1"
    echo "\n=== Starting HTTP server on port $port in container ==="

    # Start HTTP server in background and give it time to start
    ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -p 2222 "$MACHINE_NAME@localhost" "sudo python3 -m http.server $port > /dev/null 2>&1 &"

    # Wait a moment for server to start
    sleep 3

    # Test that server is running
    server_check=$(ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -p 2222 "$MACHINE_NAME@localhost" "sudo netstat -ln | grep :$port || echo 'NOT_FOUND'")

    if [[ "$server_check" =~ ":$port" ]]; then
        echo "✅ HTTP server started successfully on port $port"
        return 0
    else
        echo "❌ HTTP server failed to start on port $port"
        return 1
    fi
}

# Function to stop HTTP servers
stop_http_servers() {
    echo "\n=== Stopping HTTP servers in container ==="
    ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -p 2222 "$MACHINE_NAME@localhost" "sudo pkill -f 'python3 -m http.server' || true"
    sleep 1
    echo "✅ HTTP servers stopped"
}

# Function to set route
set_route() {
    local port="$1"
    local share="$2"
    local description="$3"

    echo "\n=== Setting route: $description ==="

    ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -p 2222 localhost "route $MACHINE_NAME --port=$port --$share"

    if [ $? -eq 0 ]; then
        echo "✅ Route set successfully (port: $port, share: $share)"
        return 0
    else
        echo "❌ Failed to set route"
        return 1
    fi
}

echo "\n🚀 Starting proxy functionality test..."

# Clean up any existing HTTP servers first
stop_http_servers

# Test 1: Start HTTP server on port 80
start_http_server 80

# Test 2: Check default route (should be private)
check_route 80 private "Default route should be port 80, private"

# Test 3: Test private access (may require auth - we'll check for redirect or auth-related response)
echo "\n=== Testing private access (may require authentication) ==="
# For private routes, we might get a redirect or auth challenge
curl -v "$PROXY_URL" 2>&1 | grep -E "(Location:|HTTP/|<title>|Directory listing|Index of)" | head -5

# Test 4: Make route public
set_route 80 public "Make route public"

# Test 5: Verify route is public
check_route 80 public "Route should now be public"

# Test 6: Test public access (should work without auth)
test_http_response "$PROXY_URL" "(Directory listing|Index of|<title>)" "Public access should work without authentication"

# Test 7: Change to port 8000
stop_http_servers
start_http_server 8000
set_route 8000 public "Change to port 8000, public"

# Test 8: Test access with new port
test_http_response "$PROXY_URL" "(Directory listing|Index of|<title>)" "Access should work with port 8000"

# Test 9: Make route private again
set_route 8000 private "Make route private again"

# Test 10: Verify route is private
check_route 8000 private "Route should be private"

# Test 11: Test that private access requires auth again
echo "\n=== Testing that private access requires authentication ==="
curl -v "$PROXY_URL" 2>&1 | grep -E "(Location:|HTTP/|401|403|login|auth)" | head -5

# Clean up
stop_http_servers

echo "\n\n🎉 Proxy functionality test completed!"
echo "Manual verification may be needed for authentication flows."
echo "Check the output above for any failed tests (❌)."
