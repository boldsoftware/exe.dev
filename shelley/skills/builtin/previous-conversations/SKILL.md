---
name: previous-conversations
description: Use when the user references a previous conversation, asks you to continue earlier work, or you need to look up what was discussed before.
---

Shelley stores conversation history in a SQLite database.

First, locate the database:

```bash
DB="${SHELLEY_DB:-$HOME/.config/shelley/shelley.db}"
```

## List recent conversations

```bash
sqlite3 "$DB" "SELECT conversation_id, slug, datetime(created_at, 'localtime') as created, datetime(updated_at, 'localtime') as updated FROM conversations ORDER BY updated_at DESC LIMIT 20;"
```

## Get messages from a conversation

Replace CONVERSATION_ID with the actual ID:

```bash
sqlite3 "$DB" "SELECT CASE type WHEN 'user' THEN 'User' ELSE 'Agent' END, substr(json_extract(llm_data, '$.Content[0].Text'), 1, 500) FROM messages WHERE conversation_id='CONVERSATION_ID' AND type IN ('user', 'agent') AND json_extract(llm_data, '$.Content[0].Type') = 2 AND json_extract(llm_data, '$.Content[0].Text') != '' ORDER BY sequence_id;"
```

## Search conversations by slug

```bash
sqlite3 "$DB" "SELECT conversation_id, slug FROM conversations WHERE slug LIKE '%SEARCH_TERM%';"
```
