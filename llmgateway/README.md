# Testing notes

1. Set up a proxy: curl --insecure http://localhost:1234/

2. Run this manually:

curl http://localhost:1234/_/gateway/anthropic/v1/messages   --header "Authorization: $(sudo /usr/local/bin/generate-gateway-token)"   --header "content-type: application/json"   --header "anthropic-version: 2023-06-01"   --data '{
    "model": "claude-3-5-sonnet-20241022",
    "max_tokens": 1024,
    "messages": [
      {
        "role": "user",
        "content": "Hello, Claude!"
      }
    ]
  }'
