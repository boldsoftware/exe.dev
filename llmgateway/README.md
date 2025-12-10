# Testing notes

In dev mode, test the gateway with the X-Exedev-Box header:

```
curl http://localhost:8080/_/gateway/anthropic/v1/messages \
  --header "X-Exedev-Box: testbox" \
  --header "content-type: application/json" \
  --header "anthropic-version: 2023-06-01" \
  --data '{
    "model": "claude-3-5-sonnet-20241022",
    "max_tokens": 1024,
    "messages": [
      {
        "role": "user",
        "content": "Hello, Claude!"
      }
    ]
  }'
```
