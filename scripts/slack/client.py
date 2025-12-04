import json
import os
import sys
import typing as t
import urllib.error
import urllib.request


class SlackError(RuntimeError):
    """Raised when the Slack API reports an error."""


class SlackClient:
    _API_BASE = "https://slack.com/api/"

    def __init__(self, token: str) -> None:
        token = (token or "").strip()
        if not token:
            raise SlackError("EXE_SLACK_BOT_TOKEN must be set")
        self._token = token

    def api(self, method: str, payload: t.Dict[str, t.Any]) -> t.Dict[str, t.Any]:
        body = json.dumps(payload).encode("utf-8")
        req = urllib.request.Request(
            f"{self._API_BASE}{method}",
            data=body,
            headers={
                "Authorization": f"Bearer {self._token}",
                "Content-Type": "application/json; charset=utf-8",
            },
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
        mrkdwn: t.Optional[bool] = None,
        thread_ts: t.Optional[str] = None,
    ) -> str:
        """Post a message and return its timestamp (ts)."""
        payload: t.Dict[str, t.Any] = {
            "channel": channel_id,
            "text": text,
        }
        if mrkdwn is not None:
            payload["mrkdwn"] = mrkdwn
        if thread_ts is not None:
            payload["thread_ts"] = thread_ts
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


def ensure_token() -> str:
    token = os.environ.get("EXE_SLACK_BOT_TOKEN", "").strip()
    if not token:
        print("EXE_SLACK_BOT_TOKEN must be set", file=sys.stderr)
        sys.exit(1)
    return token
