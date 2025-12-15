# Shelley - A Coding Agent for exe.dev

Shelley is a conversational AI coding agent that provides a web interface
for AI-powered coding assistance.

See also ARCHITECTURE.md for architectural details.

## Dev Tricks

If you want to see how mobile looks, and you're on your home
network where you've got mDNS working fine, you can
run 
  socat TCP-LISTEN:9001,fork TCP:localhost:9000
and then have your phone go to http://hostname.local:9001/

## CLI Usage

Shelley can be used as a command-line tool with the following commands:

### Global Flags

- `--db <path>`: Path to SQLite database file (default: "shelley.db")
- `--model <model>`: LLM model to use (use `predictable` for testing). Run `shelley models` to see available models.
- `--debug`: Enable debug logging

### Commands

#### `serve` - Start Web Server

Starts the web server for the browser-based interface.

```bash
shelley serve --port 9000
```

Flags:
- `--port <port>`: Port to listen on (default: 9000)

#### `models` - List Supported Models

Lists all supported models and their required environment variables.

```bash
shelley models
```

### Examples

```bash
# Start the web server
shelley serve --port 8080

# List supported models
shelley models

## Models and API Keys

Use `shelley models` to see supported models, whether they are ready, and the environment variables required for each.

Common env vars:

- `ANTHROPIC_API_KEY`: Required for Claude models.
- `OPENAI_API_KEY`: Required for OpenAI models.
- `FIREWORKS_API_KEY`: Required for Fireworks models.

Notes:

- Run `shelley models` to see which model is the default and which are available.
- `predictable` is a built-in test model and requires no API keys.
```
