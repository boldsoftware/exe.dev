# Missive API Reference

## Authentication

```
Authorization: Bearer <EXE_MISSIVE_API_KEY>
```

Token prefix: `missive_pat-...`. Generated from Missive preferences > API tab. Requires Productive plan.

## Base URL

```
https://public.missiveapp.com/v1/
```

All responses are JSON. POST requests require `Content-Type: application/json`. Successful POSTs may return 201 with no body.

## Ontology

```
Organization
├── Teams (no CRUD endpoints; managed in Missive UI)
├── Shared Labels (org-wide tags on conversations)
├── Contact Books
│   ├── Contacts
│   └── Contact Groups (groups or organizations)
├── Responses (reusable message templates; org, team, or personal scope)
└── Conversations (the central object)
    ├── Messages — received communications (email/SMS/WhatsApp/custom); immutable
    ├── Comments — internal team-only notes; can have mentions and tasks
    ├── Drafts — outgoing messages; editable until sent
    └── Posts — automation/integration notifications; metadata-rich
```

Key distinctions:
- **Message** vs **Comment**: Messages are external communications; comments are internal team annotations invisible to the customer.
- **Posts**: System-generated traces from automations/integrations, not human-authored messages.
- **Responses**: Canned reply templates, not conversation responses.
- **Shared Labels**: Org-level tags for categorizing conversations (distinct from contact groups).

## Read Endpoints

### Organizations
- `GET /v1/organizations` — list orgs the token user belongs to (limit/offset)

### Conversations
- `GET /v1/conversations` — list conversations visible to token owner
  - Pagination: **cursor-based on timestamps**, not offset. Pass `until=<last_activity_at of oldest result>` for next page. May return more than `limit` items.
  - `limit` (default 25, max 50)
  - Mailbox filters (pass `true`): `inbox`, `all`, `assigned`, `closed`, `snoozed`, `flagged`, `trashed`, `junked`, `drafts`
  - Team filters: `team_inbox`, `team_closed`, `team_all`
  - Contact filters (mutually exclusive): `email`, `domain`, `contact_organization`
  - Other: `organization`, `shared_label`
- `GET /v1/conversations/:id` — single conversation
- `GET /v1/conversations/:id/messages` — messages in conversation (limit max 10, cursor via `until`)
- `GET /v1/conversations/:id/comments` — internal comments (limit max 10)
- `GET /v1/conversations/:id/drafts` — drafts (limit max 10)
- `GET /v1/conversations/:id/posts` — posts (limit max 10)

### Messages
- `GET /v1/messages/:id` — single message with headers, body, attachments
- `GET /v1/messages/:id1,:id2,:id3` — batch fetch by comma-separated IDs
- `GET /v1/messages?email_message_id=<Message-ID>` — find by email Message-ID header

### Contacts
- `GET /v1/contacts` — list; supports `search` param (searches all contact fields), `contact_book`, `modified_since`
- `GET /v1/contacts/:id` — single contact

### Contact Books & Groups
- `GET /v1/contact_books` — list (limit/offset, max 200)
- `GET /v1/contact_groups` — list; requires `contact_book` and `kind` ("group" or "organization")

### Responses (templates)
- `GET /v1/responses` — list templates (limit/offset, max 200); filter by `organization`
- `GET /v1/responses/:id`

### Analytics
- `GET /v1/analytics/reports/:id` — fetch a generated report

## Search

There is **no dedicated search endpoint**. Searching is done via filter params on list endpoints:
- Contacts: `search` param (full-text across all contact fields)
- Conversations: `email`, `domain`, `contact_organization` (mutually exclusive; contact-based filtering only — no full-text conversation search via API)

## Conversation State Mutations (non-RESTful)

There is **no PATCH endpoint for conversations**. State changes (close, assign, label, move) are done as side effects via action parameters on `POST /v1/drafts`, `POST /v1/messages`, or post creation. Actions include:
- `close`, `add_to_inbox`, `add_to_team_inbox`
- `add_assignees`, `remove_assignees`
- `add_shared_labels`, `remove_shared_labels`

This means you must create a draft/message/post to modify conversation state — you can't just PATCH a conversation.

## Write Endpoints (for reference)

- `POST /v1/drafts` — create draft (set `send: true` to send immediately, `send_at` to schedule)
- `DELETE /v1/drafts/:id`
- `POST /v1/messages` — create incoming message (custom channels only)
- `POST /v1/contacts` — create contact(s)
- `PATCH /v1/contacts/:id1,:id2` — update contact(s)
- `POST /v1/conversations/:id/merge` — merge conversations
- `POST /v1/responses` — create template(s)
- `PATCH /v1/responses/:id1,:id2` — update template(s)
- `DELETE /v1/responses/:id1,:id2`
- `POST /v1/analytics/reports` — create report

## Addressing by Channel

- **Email**: `from_field`/`to_fields` with `{address, name}`
- **SMS/WhatsApp**: phone as `"+" + digits`; specify type if account handles both
- **Messenger/Instagram**: user `id` only
- **Custom channels**: `{id, username, name}` triplet

## Gotchas

- Conversations where the token user is a **guest** return only `id` and `last_activity_at`.
- Messages endpoint max limit is **10** per page (not configurable higher).
- No documented rate limits, but the low max-limit values suggest they want you to be gentle.
- Teams have no API endpoints — they're visible as fields on conversations/drafts but managed in the UI only.
- Resource IDs for mailboxes/labels/teams can be found in Missive UI under Settings > API > Resource IDs.
