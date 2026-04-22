package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// defaultMissiveAPIBase is Missive's public REST API. When we have a direct
// bearer token we hit this directly.
const defaultMissiveAPIBase = "https://public.missiveapp.com/v1"

// defaultMissiveIntegrationBase is the exe.dev integration proxy hostname.
// Used when no direct token is configured; the proxy injects auth.
const defaultMissiveIntegrationBase = "https://missive.int.exe.xyz/v1"

// errRateLimited is returned by the client when Missive responds with 429 and
// we want the caller to back off (instead of the client silently retrying).
var errRateLimited = errors.New("missive: rate limited")

// missiveConfig holds the resolved auth + endpoint for a Missive client.
type missiveConfig struct {
	Base  string // e.g. https://public.missiveapp.com/v1
	Token string // optional bearer; empty when using the integration proxy
}

// resolveMissive picks the right Missive endpoint based on env vars:
//   - EXE_MISSIVE_API_KEY: direct Missive PAT, used against
//     EXE_MISSIVE_BASE (or defaultMissiveAPIBase).
//   - otherwise: assume the `missive` exe.dev integration is attached and
//     send unauthenticated requests to EXE_MISSIVE_BASE (or
//     defaultMissiveIntegrationBase), where the integration proxy adds auth.
//
// Returns ok=false if no usable configuration can be inferred.
func resolveMissive() (missiveConfig, bool) {
	token := strings.TrimSpace(os.Getenv("EXE_MISSIVE_API_KEY"))
	base := strings.TrimRight(strings.TrimSpace(os.Getenv("EXE_MISSIVE_BASE")), "/")
	if token != "" {
		if base == "" {
			base = defaultMissiveAPIBase
		}
		return missiveConfig{Base: base, Token: token}, true
	}
	if base == "" {
		base = defaultMissiveIntegrationBase
	}
	return missiveConfig{Base: base}, true
}

type missiveClient struct {
	cfg  missiveConfig
	http *http.Client
}

func newMissiveClient(cfg missiveConfig) *missiveClient {
	return &missiveClient{
		cfg:  cfg,
		http: &http.Client{Timeout: 60 * time.Second},
	}
}

// post sends a JSON POST body to Missive. Same retry/429 handling as get.
func (c *missiveClient) post(ctx context.Context, path string, body, out any) error {
	buf, err := json.Marshal(body)
	if err != nil {
		return err
	}
	u := c.cfg.Base + path
	var lastErr error
	for attempt := 0; attempt < 4; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(string(buf)))
		if err != nil {
			return err
		}
		if c.cfg.Token != "" {
			req.Header.Set("Authorization", "Bearer "+c.cfg.Token)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		resp, err := c.http.Do(req)
		if err != nil {
			lastErr = err
			time.Sleep(time.Duration(1<<attempt) * time.Second)
			continue
		}
		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode == 429 {
			return fmt.Errorf("%w: POST %s HTTP 429", errRateLimited, path)
		}
		if resp.StatusCode >= 500 {
			retry := 2
			if ra := resp.Header.Get("Retry-After"); ra != "" {
				if n, err := strconv.Atoi(ra); err == nil {
					retry = n
				}
			}
			lastErr = fmt.Errorf("missive POST %s: HTTP %d", path, resp.StatusCode)
			time.Sleep(time.Duration(retry) * time.Second)
			continue
		}
		if resp.StatusCode >= 400 {
			return fmt.Errorf("missive POST %s: HTTP %d: %s", path, resp.StatusCode, truncate(string(respBody), 400))
		}
		if out == nil {
			return nil
		}
		return json.Unmarshal(respBody, out)
	}
	if lastErr == nil {
		lastErr = errors.New("missive: exhausted retries")
	}
	return lastErr
}

// postComment creates a Missive "post" (internal comment/note) on the given
// conversation. markdown is the rendered body; notification is what shows in
// the team feed. Returns the new post id.
func (c *missiveClient) postComment(ctx context.Context, conversationID, markdown, notificationTitle, notificationBody string) (string, error) {
	body := map[string]any{
		"posts": map[string]any{
			"conversation": conversationID,
			"markdown":     markdown,
			"notification": map[string]string{
				"title": notificationTitle,
				"body":  notificationBody,
			},
		},
	}
	var resp struct {
		Posts struct {
			ID string `json:"id"`
		} `json:"posts"`
	}
	if err := c.post(ctx, "/posts", body, &resp); err != nil {
		return "", err
	}
	return resp.Posts.ID, nil
}

func (c *missiveClient) get(ctx context.Context, path string, params url.Values, out any) error {
	u := c.cfg.Base + path
	if len(params) > 0 {
		u += "?" + params.Encode()
	}
	var lastErr error
	for attempt := 0; attempt < 4; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return err
		}
		if c.cfg.Token != "" {
			req.Header.Set("Authorization", "Bearer "+c.cfg.Token)
		}
		req.Header.Set("Accept", "application/json")
		resp, err := c.http.Do(req)
		if err != nil {
			lastErr = err
			time.Sleep(time.Duration(1<<attempt) * time.Second)
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode == 429 {
			// Surface 429 to the caller immediately so the poll loop can back off
			// its whole cycle (rather than stalling this request and eating CPU).
			return fmt.Errorf("%w: %s HTTP 429", errRateLimited, path)
		}
		if resp.StatusCode >= 500 {
			retry := 2
			if ra := resp.Header.Get("Retry-After"); ra != "" {
				if n, err := strconv.Atoi(ra); err == nil {
					retry = n
				}
			}
			lastErr = fmt.Errorf("missive %s: HTTP %d", path, resp.StatusCode)
			time.Sleep(time.Duration(retry) * time.Second)
			continue
		}
		if resp.StatusCode >= 400 {
			return fmt.Errorf("missive %s: HTTP %d: %s", path, resp.StatusCode, truncate(string(body), 400))
		}
		return json.Unmarshal(body, out)
	}
	if lastErr == nil {
		lastErr = errors.New("missive: exhausted retries")
	}
	return lastErr
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

type missiveConversation struct {
	ID             string            `json:"id"`
	Subject        string            `json:"subject"`
	LatestSubject  string            `json:"latest_message_subject"`
	CreatedAt      float64           `json:"created_at"`
	LastActivityAt float64           `json:"last_activity_at"`
	Closed         *bool             `json:"-"`
	Assignees      []missiveAssignee `json:"assignees"`
	SharedLabels   []missiveLabel    `json:"shared_labels"`
	Team           *missiveTeam      `json:"team"`
	Raw            json.RawMessage   `json:"-"`
}

type missiveAssignee struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}
type missiveLabel struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
type missiveTeam struct {
	Name string `json:"name"`
}

func (c *missiveClient) listConversations(ctx context.Context, params url.Values) ([]missiveConversation, []json.RawMessage, error) {
	var resp struct {
		Conversations []json.RawMessage `json:"conversations"`
	}
	if err := c.get(ctx, "/conversations", params, &resp); err != nil {
		return nil, nil, err
	}
	convs := make([]missiveConversation, 0, len(resp.Conversations))
	for _, raw := range resp.Conversations {
		var conv missiveConversation
		if err := json.Unmarshal(raw, &conv); err != nil {
			continue
		}
		conv.Raw = raw
		convs = append(convs, conv)
	}
	return convs, resp.Conversations, nil
}

type missiveMessageMeta struct {
	ID          string           `json:"id"`
	Subject     string           `json:"subject"`
	Preview     string           `json:"preview"`
	DeliveredAt float64          `json:"delivered_at"`
	FromField   *missiveAddress  `json:"from_field"`
	ToFields    []missiveAddress `json:"to_fields"`
	Body        string           `json:"body"`
}

type missiveAddress struct {
	Address string `json:"address"`
	Name    string `json:"name"`
}

func (c *missiveClient) listMessages(ctx context.Context, convID, until string) ([]missiveMessageMeta, []json.RawMessage, error) {
	params := url.Values{}
	params.Set("limit", "10")
	if until != "" {
		params.Set("until", until)
	}
	var resp struct {
		Messages []json.RawMessage `json:"messages"`
	}
	if err := c.get(ctx, "/conversations/"+convID+"/messages", params, &resp); err != nil {
		return nil, nil, err
	}
	if len(resp.Messages) == 0 {
		return nil, nil, nil
	}
	metas := make([]missiveMessageMeta, 0, len(resp.Messages))
	ids := make([]string, 0, len(resp.Messages))
	rawByID := map[string]json.RawMessage{}
	for _, raw := range resp.Messages {
		var m missiveMessageMeta
		if err := json.Unmarshal(raw, &m); err != nil || m.ID == "" {
			continue
		}
		metas = append(metas, m)
		ids = append(ids, m.ID)
		rawByID[m.ID] = raw
	}
	if len(ids) == 0 {
		return nil, nil, nil
	}
	// Batch-fetch full bodies. Missive returns an object for a single id and
	// an array for multiple, per existing panopticon client observations.
	fullResp := map[string]json.RawMessage{}
	if err := c.get(ctx, "/messages/"+strings.Join(ids, ","), nil, &fullResp); err == nil {
		if raw, ok := fullResp["messages"]; ok && len(raw) > 0 {
			// Try array first.
			var arr []json.RawMessage
			if err := json.Unmarshal(raw, &arr); err != nil {
				arr = []json.RawMessage{raw}
			}
			for _, r := range arr {
				var m missiveMessageMeta
				if err := json.Unmarshal(r, &m); err != nil || m.ID == "" {
					continue
				}
				rawByID[m.ID] = r
				// overlay body+from/to into metas
				for i := range metas {
					if metas[i].ID == m.ID {
						if m.Body != "" {
							metas[i].Body = m.Body
						}
						if m.FromField != nil {
							metas[i].FromField = m.FromField
						}
						if len(m.ToFields) > 0 {
							metas[i].ToFields = m.ToFields
						}
					}
				}
			}
		}
	}
	raws := make([]json.RawMessage, len(metas))
	for i, m := range metas {
		raws[i] = rawByID[m.ID]
	}
	return metas, raws, nil
}

type missiveComment struct {
	ID        string           `json:"id"`
	Body      string           `json:"body"`
	CreatedAt float64          `json:"created_at"`
	Author    *missiveAssignee `json:"author"`
}

func (c *missiveClient) listComments(ctx context.Context, convID string) ([]missiveComment, []json.RawMessage, error) {
	params := url.Values{}
	params.Set("limit", "10")
	var resp struct {
		Comments []json.RawMessage `json:"comments"`
	}
	if err := c.get(ctx, "/conversations/"+convID+"/comments", params, &resp); err != nil {
		return nil, nil, err
	}
	comments := make([]missiveComment, 0, len(resp.Comments))
	raws := make([]json.RawMessage, 0, len(resp.Comments))
	for _, raw := range resp.Comments {
		var c missiveComment
		if err := json.Unmarshal(raw, &c); err != nil || c.ID == "" {
			continue
		}
		comments = append(comments, c)
		raws = append(raws, raw)
	}
	return comments, raws, nil
}
