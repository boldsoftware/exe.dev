#!/bin/bash

# Manual test script for Shelley server
# Usage: ./test_manual.sh [port]

set -e

PORT=${1:-8080}
BASE_URL="http://localhost:$PORT"

echo "=== Shelley Manual Test Script ==="
echo "Testing server at $BASE_URL"
echo

# Function to make HTTP requests with better error handling
make_request() {
    local method=$1
    local url=$2
    local data=$3

    echo "Making $method request to $url"
    if [ -n "$data" ]; then
        echo "Request body: $data"
    fi

    if [ -n "$data" ]; then
        curl -s -X "$method" -H "Content-Type: application/json" -d "$data" "$url" || echo "Request failed"
    else
        curl -s -X "$method" "$url" || echo "Request failed"
    fi

    echo
    echo "---"
    echo
}

echo "1. Testing server health by listing conversations..."
make_request "GET" "$BASE_URL/conversations"

echo "2. Creating a test conversation..."
echo "   Note: This test assumes a conversation exists. If not, create one via the database or modify the server to auto-create."
echo

echo "3. Testing with a sample conversation ID (replace with real ID)..."
echo "   For a real test, first start the server, create a conversation via the database,"
echo "   then use that conversation ID in the following requests."
echo
echo "   Example conversation creation (using sqlite3):"
echo "   sqlite3 shelley.db \"INSERT INTO conversations (conversation_id, slug) VALUES ('test-123', 'manual-test');\""
echo
echo "   Then test chat:"
echo "   curl -X POST -H 'Content-Type: application/json' -d '{\"message\": \"Hello, how are you?\"}' $BASE_URL/conversation/test-123/chat"
echo
echo "   And get messages:"
echo "   curl $BASE_URL/conversation/test-123"
echo
echo "   And test streaming:"
echo "   curl $BASE_URL/conversation/test-123/stream"
echo

echo "4. Instructions for testing with Anthropic API:"
echo "   1. Set ANTHROPIC_API_KEY environment variable with a valid key"
echo "   2. Start server: cd cmd/shelley && ./shelley --port=$PORT"
echo "   3. Create a conversation and send messages as shown above"
echo

echo "5. Testing server responsiveness..."
echo "   If server is running, this should return an empty conversations list:"
make_request "GET" "$BASE_URL/conversations?limit=1"

echo "=== Manual test complete ==="
echo "For full testing with real conversations, use the commands shown above."
