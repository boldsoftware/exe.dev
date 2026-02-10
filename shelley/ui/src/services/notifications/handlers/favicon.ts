import type { NotificationEvent } from "../../../types";
import { setFaviconStatus } from "../../favicon";

export function faviconNotificationHandler(event: NotificationEvent): void {
  switch (event.type) {
    case "agent_done":
    case "agent_error":
      setFaviconStatus("ready");
      break;
  }
}
