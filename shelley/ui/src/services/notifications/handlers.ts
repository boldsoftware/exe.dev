import type { NotificationEvent } from "../../types";
import { isChannelEnabled } from "./preferences";

type NotificationHandler = (event: NotificationEvent) => void;

const handlers: Map<string, NotificationHandler> = new Map();

export function registerHandler(
  name: string,
  handler: NotificationHandler,
): void {
  handlers.set(name, handler);
}

export function handleNotificationEvent(event: NotificationEvent): void {
  for (const [name, handler] of handlers) {
    if (!isChannelEnabled(name, event.type)) continue;
    try {
      handler(event);
    } catch (err) {
      console.error(`Notification handler ${name} failed:`, err);
    }
  }
}
