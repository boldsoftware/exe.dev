package deploy

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	slackAPIBase        = "https://slack.com/api/"
	slackChannelProd    = "ship"
	slackChannelStaging = "boat"

	githubCommitURL = "https://github.com/boldsoftware/exe/commit/"
)

// SlackNotifier posts deploy notifications to Slack using the Bot API.
type SlackNotifier struct {
	token  string
	log    *slog.Logger
	client *http.Client

	// mu protects msgs (deploy ID → channel:ts for reaction tracking).
	mu   sync.Mutex
	msgs map[string]slackRef
}

type slackRef struct {
	channelID string
	ts        string
}

// NewSlackNotifier creates a Slack notifier. Token is a Slack Bot token
// (xoxb-...). Returns nil if token is empty (notifications disabled).
func NewSlackNotifier(token string, log *slog.Logger) *SlackNotifier {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil
	}
	return &SlackNotifier{
		token:  token,
		log:    log,
		client: &http.Client{Timeout: 15 * time.Second},
		msgs:   make(map[string]slackRef),
	}
}

func (s *SlackNotifier) channelForStage(stage string) string {
	if stage == "staging" {
		return slackChannelStaging
	}
	return slackChannelProd
}

func (s *SlackNotifier) DeployStarted(st Status) {
	channel := s.channelForStage(st.Stage)
	channelID, err := s.findChannelID(channel)
	if err != nil {
		s.log.Warn("slack: find channel", "channel", channel, "error", err)
		return
	}

	who := st.InitiatedBy
	if who == "" {
		who = "unknown"
	}

	shaShort := st.SHA
	if len(shaShort) > 12 {
		shaShort = shaShort[:12]
	}
	shaLink := fmt.Sprintf("<%s%s|%s>", githubCommitURL, st.SHA, shaShort)

	// Compose Slack blocks similar to the existing deploy_notify.py.
	fields := []map[string]any{
		{"type": "mrkdwn", "text": fmt.Sprintf("*Process*\n%s", st.Process)},
		{"type": "mrkdwn", "text": fmt.Sprintf("*Host*\n%s", st.Host)},
		{"type": "mrkdwn", "text": fmt.Sprintf("*SHA*\n%s", shaLink)},
		{"type": "mrkdwn", "text": fmt.Sprintf("*Who*\n%s", who)},
	}
	blocks := []map[string]any{
		{"type": "section", "fields": fields},
	}

	fallback := fmt.Sprintf("Deploying %s to %s (%s by %s)", st.Process, st.Host, shaShort, who)
	ts, err := s.postMessage(channelID, fallback, blocks)
	if err != nil {
		s.log.Warn("slack: post deploy start", "error", err)
		return
	}

	s.mu.Lock()
	s.msgs[st.ID] = slackRef{channelID: channelID, ts: ts}
	s.mu.Unlock()

	s.log.Info("slack: deploy notification posted", "channel", channel, "deploy_id", st.ID)
}

func (s *SlackNotifier) DeployFinished(st Status) {
	s.mu.Lock()
	ref, ok := s.msgs[st.ID]
	if ok {
		delete(s.msgs, st.ID)
	}
	s.mu.Unlock()

	if !ok {
		// No start message was posted (channel lookup may have failed).
		return
	}

	emoji := "white_check_mark"
	if st.State == "failed" {
		emoji = "x"
	}

	if err := s.addReaction(ref.channelID, ref.ts, emoji); err != nil {
		s.log.Warn("slack: add reaction", "emoji", emoji, "error", err)
	}
}

// ---- Slack API helpers ----

func (s *SlackNotifier) slackAPI(method string, payload map[string]any) (map[string]any, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodPost, slackAPIBase+method, strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+s.token)
	req.Header.Set("Content-Type", "application/json; charset=utf-8")

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("slack %s: %w", method, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("slack %s: read body: %w", method, err)
	}

	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, fmt.Errorf("slack %s: decode: %w", method, err)
	}
	if ok, _ := data["ok"].(bool); !ok {
		errMsg, _ := data["error"].(string)
		return nil, fmt.Errorf("slack %s: %s", method, errMsg)
	}
	return data, nil
}

func (s *SlackNotifier) findChannelID(name string) (string, error) {
	name = strings.TrimPrefix(name, "#")
	var cursor string
	for {
		payload := map[string]any{
			"exclude_archived": true,
			"limit":            200,
			"types":            "public_channel,private_channel",
		}
		if cursor != "" {
			payload["cursor"] = cursor
		}
		data, err := s.slackAPI("conversations.list", payload)
		if err != nil {
			return "", err
		}
		channels, _ := data["channels"].([]any)
		for _, ch := range channels {
			m, _ := ch.(map[string]any)
			if n, _ := m["name"].(string); strings.EqualFold(n, name) {
				if id, _ := m["id"].(string); id != "" {
					return id, nil
				}
			}
		}
		meta, _ := data["response_metadata"].(map[string]any)
		cursor, _ = meta["next_cursor"].(string)
		if cursor == "" {
			break
		}
	}
	return "", fmt.Errorf("channel #%s not found", name)
}

func (s *SlackNotifier) postMessage(channelID, text string, blocks []map[string]any) (string, error) {
	payload := map[string]any{
		"channel": channelID,
		"text":    text,
	}
	if len(blocks) > 0 {
		payload["blocks"] = blocks
	}
	data, err := s.slackAPI("chat.postMessage", payload)
	if err != nil {
		return "", err
	}
	ts, _ := data["ts"].(string)
	return ts, nil
}

func (s *SlackNotifier) addReaction(channelID, ts, emoji string) error {
	_, err := s.slackAPI("reactions.add", map[string]any{
		"channel":   channelID,
		"timestamp": ts,
		"name":      emoji,
	})
	return err
}
