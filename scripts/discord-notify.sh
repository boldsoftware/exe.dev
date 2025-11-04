#!/bin/bash

WEBHOOK_URL="https://discord.com/api/webhooks/1435095844142710887/oTAg9sAXXfR8rZsPUg8ZTq3sWu6YMkY4XSV5LNJ9n2-ebk2qrxFiIJetuOntbSYAiMwA"
MESSAGE="$1"
JSON_PAYLOAD="{\"content\":\"$MESSAGE\"}"
curl -s -H "Content-Type: application/json" -X POST -d "$JSON_PAYLOAD" "$WEBHOOK_URL"
