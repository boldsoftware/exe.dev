import json
import os
import sys
import typing as t
import urllib.error
import urllib.request


class SlackError(RuntimeError):
    """Raised when the Slack API reports an error."""


class SlackClient:
    def __init__(self, token: str = "", base_url: str = "") -> None:
        self._token = (token or "").strip()
        if base_url:
            self._api_base = base_url.rstrip("/") + "/api/"
        else:
            self._api_base = "https://slack.com/api/"

    def api(self, method: str, payload: t.Dict[str, t.Any]) -> t.Dict[str, t.Any]:
        body = json.dumps(payload).encode("utf-8")
        headers = {"Content-Type": "application/json; charset=utf-8"}
        if self._token:
            headers["Authorization"] = f"Bearer {self._token}"
        req = urllib.request.Request(
            f"{self._api_base}{method}",
            data=body,
            headers=headers,
            method="POST",
        )
        try:
            with urllib.request.urlopen(req, timeout=30) as resp:
                raw = resp.read()
        except urllib.error.HTTPError as exc:
            details = exc.read().decode("utf-8", "ignore")
            raise SlackError(f"Slack API {method} failed: HTTP {exc.code} {details}") from exc
        try:
            data = json.loads(raw)
        except json.JSONDecodeError as exc:
            raise SlackError(f"Slack API {method} returned invalid JSON: {raw!r}") from exc
        if not data.get("ok"):
            raise SlackError(f"Slack API {method} error: {data.get('error', 'unknown')}")
        return data

    def find_channel_id(self, channel_name: str) -> str:
        channel_name = channel_name.lstrip("#")
        cursor: t.Optional[str] = None
        while True:
            payload: t.Dict[str, t.Any] = {
                "exclude_archived": True,
                "limit": 200,
                "types": "public_channel,private_channel",
            }
            if cursor:
                payload["cursor"] = cursor
            resp = self.api("conversations.list", payload)
            for channel in resp.get("channels", []):
                if (channel.get("name") or "").lower() == channel_name.lower():
                    channel_id = channel.get("id")
                    if channel_id:
                        return channel_id
            cursor = resp.get("response_metadata", {}).get("next_cursor")
            if not cursor:
                break
        raise SlackError(f"channel #{channel_name} not found or bot lacks access")

    def iter_history(self, channel_id: str, limit: int = 200) -> t.Iterator[t.Dict[str, t.Any]]:
        cursor: t.Optional[str] = None
        while True:
            payload: t.Dict[str, t.Any] = {
                "channel": channel_id,
                "limit": limit,
            }
            if cursor:
                payload["cursor"] = cursor
            resp = self.api("conversations.history", payload)
            for message in resp.get("messages", []):
                yield message
            cursor = resp.get("response_metadata", {}).get("next_cursor")
            if not cursor:
                break

    def post_message(
        self,
        channel_id: str,
        text: str,
        *,
        blocks: t.Optional[t.List[t.Dict[str, t.Any]]] = None,
        mrkdwn: t.Optional[bool] = None,
        thread_ts: t.Optional[str] = None,
        unfurl_links: t.Optional[bool] = None,
        unfurl_media: t.Optional[bool] = None,
    ) -> str:
        """Post a message and return its timestamp (ts)."""
        payload: t.Dict[str, t.Any] = {
            "channel": channel_id,
            "text": text,
        }
        if blocks is not None:
            payload["blocks"] = blocks
        if mrkdwn is not None:
            payload["mrkdwn"] = mrkdwn
        if thread_ts is not None:
            payload["thread_ts"] = thread_ts
        if unfurl_links is not None:
            payload["unfurl_links"] = unfurl_links
        if unfurl_media is not None:
            payload["unfurl_media"] = unfurl_media
        resp = self.api("chat.postMessage", payload)
        return resp.get("ts", "")

    def update_message(self, channel_id: str, ts: str, text: str) -> None:
        self.api(
            "chat.update",
            {
                "channel": channel_id,
                "ts": ts,
                "text": text,
            },
        )

    def add_reaction(self, channel_id: str, ts: str, emoji: str) -> None:
        """Add an emoji reaction to a message. Emoji should be without colons (e.g., 'white_check_mark')."""
        self.api(
            "reactions.add",
            {
                "channel": channel_id,
                "timestamp": ts,
                "name": emoji,
            },
        )


def ensure_token() -> str:
    """Read Slack token from env. Returns empty string if EXE_SLACK_URL is set instead."""
    token = os.environ.get("EXE_SLACK_BOT_TOKEN", "").strip()
    url = os.environ.get("EXE_SLACK_URL", "").strip()
    if not token and not url:
        print("EXE_SLACK_BOT_TOKEN or EXE_SLACK_URL must be set", file=sys.stderr)
        sys.exit(1)
    return token
