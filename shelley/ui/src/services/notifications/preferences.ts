const STORAGE_KEY = "shelley-notification-prefs";

export interface NotificationPreferences {
  channels: {
    [channelName: string]: {
      enabled: boolean;
      events?: {
        [eventType: string]: boolean;
      };
    };
  };
}

const DEFAULT_PREFS: NotificationPreferences = {
  channels: {
    favicon: { enabled: true },
    browser: { enabled: false },
  },
};

export function getNotificationPreferences(): NotificationPreferences {
  const stored = localStorage.getItem(STORAGE_KEY);
  if (stored) {
    try {
      return JSON.parse(stored);
    } catch {
      return DEFAULT_PREFS;
    }
  }
  return DEFAULT_PREFS;
}

export function setNotificationPreferences(
  prefs: NotificationPreferences,
): void {
  localStorage.setItem(STORAGE_KEY, JSON.stringify(prefs));
}

export function setChannelEnabled(channelName: string, enabled: boolean): void {
  const prefs = getNotificationPreferences();
  if (!prefs.channels[channelName]) {
    prefs.channels[channelName] = { enabled };
  } else {
    prefs.channels[channelName].enabled = enabled;
  }
  setNotificationPreferences(prefs);
}

export function isChannelEnabled(
  channelName: string,
  eventType?: string,
): boolean {
  const prefs = getNotificationPreferences();
  const channelPrefs = prefs.channels[channelName];
  if (!channelPrefs || !channelPrefs.enabled) return false;
  if (eventType && channelPrefs.events) {
    const eventPref = channelPrefs.events[eventType];
    if (eventPref !== undefined) return eventPref;
  }
  return true;
}
