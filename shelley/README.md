# Shelley - A Coding Agent for exe.dev

Shelley is a conversational AI coding agent that provides both a web interface and command-line tools for AI-powered coding assistance.

See also ARCHITECTURE.md for architectural details.

## CLI Usage

Shelley can be used as a command-line tool with the following commands:

### Global Flags

- `--db <path>`: Path to SQLite database file (default: "shelley.db")
- `--model <model>`: LLM model to use (default: `qwen3-coder-fireworks`; use `predictable` for testing).
- `--debug`: Enable debug logging

### Commands

#### `serve` - Start Web Server

Starts the web server for the browser-based interface.

```bash
shelley serve --port 9000
```

Flags:
- `--port <port>`: Port to listen on (default: 9000)

#### `prompt` - Run Single Conversation

Runs a single conversation turn with the AI agent.

```bash
# Start a new conversation
shelley --model predictable prompt "Hello, can you help me with Python?"

# Continue an existing conversation
shelley --model predictable prompt --continue <conversation-id> "What about error handling?"
```

Flags:
- `--continue <id>`: Continue existing conversation with given ID
- `--timeout <duration>`: Timeout for LLM request (default: 30s)

#### `list` - List Conversations

Lists existing conversations.

```bash
shelley list
shelley list --limit 10 --offset 20
```

Flags:
- `--limit <n>`: Maximum number of conversations to list (default: 20)
- `--offset <n>`: Number of conversations to skip (default: 0)

#### `inspect` - Show Conversation Details

Shows detailed information about a specific conversation including all messages.

```bash
shelley inspect <conversation-id>
```

### Examples

```bash
# Start a conversation with predictable responses for testing
shelley --model predictable prompt "Write a Python function to calculate fibonacci numbers"

# List all conversations
shelley list

# Get details about a specific conversation
shelley inspect 12345678-abcd-1234-5678-123456789012

# Continue working on that conversation
shelley --model predictable prompt --continue 12345678-abcd-1234-5678-123456789012 "Add error handling to the function"

# Start the web server
shelley serve --port 8080

## Models and API Keys

Use `shelley models` to see supported models, whether they are ready, and the environment variables required for each.

Common env vars:

- `FIREWORKS_API_KEY`: Required for `qwen3-coder-fireworks` (default model).
- `OPENAI_API_KEY`: Required for OpenAI models (e.g. `openai-gpt4`).
- `ANTHROPIC_API_KEY`: Required for Claude models (e.g. `claude-sonnet-3.5`).

Notes:

- Default model is `qwen3-coder-fireworks`. If required env vars are missing, the CLI will error with guidance.
- `predictable` is a built-in test model and requires no API keys.
```
