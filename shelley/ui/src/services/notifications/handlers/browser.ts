import type { NotificationEvent } from "../../../types";

export function browserNotificationHandler(event: NotificationEvent): void {
  if (!document.hidden) return;
  if (typeof Notification === "undefined") return;
  if (Notification.permission !== "granted") return;

  switch (event.type) {
    case "agent_done":
      new Notification("Shelley", {
        body: "Agent finished",
        tag: "shelley-agent-done",
      });
      break;
    case "agent_error":
      new Notification("Shelley", {
        body: "Agent error",
        tag: "shelley-agent-error",
      });
      break;
  }
}
