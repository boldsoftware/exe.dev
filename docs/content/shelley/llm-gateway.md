---
title: LLM Gateway
description: LLM tokens included
subheading: "2. Features"
suborder: 5
published: true
---

Every `exe.dev` VM has access to the LLM Gateway, a built-in proxy to
Anthropic, OpenAI, and Fireworks APIs. Your subscription includes a monthly
token allocation, and you can purchase additional tokens at
[https://exe.dev/user](https://exe.dev/user).

See the [full list of supported models](/llm-gateway-models).

The gateway is available inside your VM at
`http://169.254.169.254/gateway/llm/provider`, where `provider` is one of
`anthropic`, `openai`, or `fireworks`. No API keys are necessary.

[Shelley](/docs/shelley/intro) uses the LLM Gateway by default, but you can
also use it directly from any program running on your VM.

## Using the gateway with curl

Point your requests at the gateway URL instead of the provider:

```
$ curl -s http://169.254.169.254/gateway/llm/anthropic/v1/messages \
    -H "content-type: application/json" \
    -H "anthropic-version: 2023-06-01" \
    -d '{
      "model": "claude-sonnet-4-5-20250929",
      "max_tokens": 256,
      "messages": [{"role": "user", "content": "Hello!"}]
    }'
```

OpenAI and Fireworks work the same way:

```
$ curl -s http://169.254.169.254/gateway/llm/openai/v1/chat/completions \
    -H "content-type: application/json" \
    -d '{
      "model": "gpt-4o",
      "messages": [{"role": "user", "content": "Hello!"}]
    }'
```

```
$ curl -s http://169.254.169.254/gateway/llm/fireworks/inference/v1/chat/completions \
    -H "content-type: application/json" \
    -d '{
      "model": "accounts/fireworks/models/llama-v3p1-8b-instruct",
      "messages": [{"role": "user", "content": "Hello!"}]
    }'
```
