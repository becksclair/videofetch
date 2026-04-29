import { enqueueSingle, fetchDownloads } from '@/lib/api';
import { loadSettings } from '@/lib/storage';
import { ext, hasServerPermission, openPrimaryPanel } from '@/lib/webext';
import type { DownloadRow, DownloadStatus } from '@/types';

const MENU_IDS = {
  link: 'videofetch_enqueue_link',
  media: 'videofetch_enqueue_media',
  page: 'videofetch_enqueue_page'
} as const;
const COMMAND_IDS = {
  openSidePanel: 'videofetch_open_sidepanel',
  enqueueCurrentPage: 'videofetch_enqueue_current_page'
} as const;

const POLL_ALARM = 'videofetch_notification_poll';
const NOTIFICATION_STATUS_KEY = 'videofetch_notification_status_v1';
const NOTIFICATION_PAGE_SIZE = 200;

const terminalStatuses = new Set<DownloadStatus>(['completed', 'error', 'canceled']);
let notificationPollInFlight: Promise<void> | null = null;

type NotificationStatusEntry = {
  status: DownloadStatus;
  url?: string;
  created_at?: string;
};

function isDownloadStatus(value: unknown): value is DownloadStatus {
  return (
    value === 'pending' ||
    value === 'downloading' ||
    value === 'paused' ||
    value === 'completed' ||
    value === 'error' ||
    value === 'canceled'
  );
}

function notificationEntryForRow(row: DownloadRow): NotificationStatusEntry {
  return {
    status: row.status,
    url: row.url,
    created_at: row.created_at
  };
}

function isSameDownload(row: DownloadRow, entry: NotificationStatusEntry): boolean {
  return (!entry.url || entry.url === row.url) && (!entry.created_at || entry.created_at === row.created_at);
}

function parseNotificationStatusEntry(value: unknown): NotificationStatusEntry | null {
  if (isDownloadStatus(value)) {
    return { status: value };
  }
  if (!value || typeof value !== 'object' || Array.isArray(value)) {
    return null;
  }
  const entry = value as Partial<NotificationStatusEntry>;
  if (!isDownloadStatus(entry.status)) {
    return null;
  }
  return {
    status: entry.status,
    url: typeof entry.url === 'string' ? entry.url : undefined,
    created_at: typeof entry.created_at === 'string' ? entry.created_at : undefined
  };
}

async function loadNotificationStatuses(): Promise<Map<number, NotificationStatusEntry>> {
  const result = await ext.storage.local.get(NOTIFICATION_STATUS_KEY);
  const raw = result[NOTIFICATION_STATUS_KEY];
  const statuses = new Map<number, NotificationStatusEntry>();
  if (!raw || typeof raw !== 'object' || Array.isArray(raw)) {
    return statuses;
  }

  for (const [id, value] of Object.entries(raw)) {
    const numericID = Number(id);
    const entry = parseNotificationStatusEntry(value);
    if (Number.isInteger(numericID) && entry) {
      statuses.set(numericID, entry);
    }
  }
  return statuses;
}

async function saveNotificationStatuses(statuses: Map<number, NotificationStatusEntry>): Promise<void> {
  const payload: Record<string, NotificationStatusEntry> = {};
  for (const [id, entry] of statuses) {
    payload[String(id)] = entry;
  }
  await ext.storage.local.set({ [NOTIFICATION_STATUS_KEY]: payload });
}

async function clearNotificationStatuses(): Promise<void> {
  await ext.storage.local.remove(NOTIFICATION_STATUS_KEY);
}

function notify(title: string, message: string): void {
  void ext.notifications.create({
    type: 'basic',
    iconUrl: 'icons/icon-128.png',
    title,
    message
  }).catch(() => undefined);
}

function notifyServerPermissionRequired(): void {
  notify('VideoFetch server access required', 'Open VideoFetch extension settings and save the server URL to grant access.');
}

async function ensureMenus(): Promise<void> {
  await ext.contextMenus.removeAll();
  ext.contextMenus.create({
    id: MENU_IDS.link,
    title: 'Send link URL to VideoFetch',
    contexts: ['link']
  });
  ext.contextMenus.create({
    id: MENU_IDS.media,
    title: 'Send media source to VideoFetch',
    contexts: ['audio', 'video']
  });
  ext.contextMenus.create({
    id: MENU_IDS.page,
    title: 'Send page URL to VideoFetch',
    contexts: ['page']
  });
}

function extractContextUrl(info: chrome.contextMenus.OnClickData, tab?: chrome.tabs.Tab): string | null {
  if (info.menuItemId === MENU_IDS.link) {
    return info.linkUrl || null;
  }
  if (info.menuItemId === MENU_IDS.media) {
    return info.srcUrl || null;
  }
  return info.pageUrl || tab?.url || null;
}

function isHTTPURL(url: string): boolean {
  return /^https?:\/\//i.test(url);
}

async function enqueueURL(url: string, source: 'context_menu' | 'toolbar' | 'command'): Promise<void> {
  if (!isHTTPURL(url)) {
    notify('VideoFetch enqueue failed', 'Selected context does not provide an http/https URL.');
    return;
  }

  try {
    const settings = await loadSettings();
    if (!(await hasServerPermission(settings.serverBaseUrl))) {
      notifyServerPermissionRequired();
      return;
    }
    await enqueueSingle(settings.serverBaseUrl, url);
    notify('VideoFetch queued', url);
    void ext.runtime.sendMessage({
      type: 'videofetch.enqueued',
      url,
      source
    }).catch(() => undefined);
  } catch (error) {
    notify('VideoFetch enqueue failed', String(error));
  }
}

function openSidePanelInCurrentWindow(): void {
  void openPrimaryPanel()
    .catch((error) => {
      notify('VideoFetch', `Failed to open side panel: ${String(error)}`);
    });
}

async function enqueueActiveTabURL(source: 'toolbar' | 'command'): Promise<void> {
  const [tab] = await ext.tabs.query({ active: true, lastFocusedWindow: true });
  if (!tab?.url) {
    notify('VideoFetch enqueue failed', 'Current tab does not have a URL.');
    return;
  }
  await enqueueURL(tab.url, source);
}

function shouldNotifyTransition(next: DownloadStatus, previous?: DownloadStatus): boolean {
  if (!previous || previous === next) {
    return false;
  }
  return terminalStatuses.has(next);
}

async function fetchNotificationRows(baseUrl: string): Promise<DownloadRow[]> {
  const [activeRows, historyRows] = await Promise.all([
    fetchDownloads(baseUrl, {
      limit: NOTIFICATION_PAGE_SIZE,
      offset: 0,
      status: 'active',
      sort: 'updated_at',
      order: 'desc'
    }),
    fetchDownloads(baseUrl, {
      limit: NOTIFICATION_PAGE_SIZE,
      offset: 0,
      status: 'history',
      sort: 'updated_at',
      order: 'desc'
    })
  ]);
  const byId = new Map<number, DownloadRow>();
  for (const row of activeRows) byId.set(row.id, row);
  for (const row of historyRows) byId.set(row.id, row);
  return Array.from(byId.values());
}

async function pollNotificationsOnce(): Promise<void> {
  const settings = await loadSettings();
  if (!settings.notificationsEnabled) {
    await clearNotificationStatuses();
    return;
  }
  if (!(await hasServerPermission(settings.serverBaseUrl))) {
    await clearNotificationStatuses();
    return;
  }

  const lastStatusById = await loadNotificationStatuses();
  const nextStatusById = new Map<number, NotificationStatusEntry>();
  for (const [id, entry] of lastStatusById) {
    if (!terminalStatuses.has(entry.status)) {
      nextStatusById.set(id, entry);
    }
  }
  const rows = await fetchNotificationRows(settings.serverBaseUrl);
  for (const row of rows) {
    const previousEntry = lastStatusById.get(row.id);
    const previous = previousEntry && isSameDownload(row, previousEntry) ? previousEntry.status : undefined;
    if (shouldNotifyTransition(row.status, previous)) {
      const title = row.title?.trim() || row.url;
      notify(`VideoFetch: ${row.status.toUpperCase()}`, `${title}`);
    }
    nextStatusById.set(row.id, notificationEntryForRow(row));
  }
  await saveNotificationStatuses(nextStatusById);
}

async function pollNotifications(): Promise<void> {
  if (notificationPollInFlight) {
    return notificationPollInFlight;
  }

  notificationPollInFlight = pollNotificationsOnce().finally(() => {
    notificationPollInFlight = null;
  });
  return notificationPollInFlight;
}

ext.runtime.onInstalled.addListener(() => {
  void ensureMenus();
  ext.alarms.create(POLL_ALARM, { periodInMinutes: 0.5 });
});

ext.runtime.onStartup.addListener(() => {
  ext.alarms.create(POLL_ALARM, { periodInMinutes: 0.5 });
});

ext.contextMenus.onClicked.addListener((info, tab) => {
  void (async () => {
    const url = extractContextUrl(info, tab);
    if (!url) {
      notify('VideoFetch enqueue failed', 'Selected context does not provide an http/https URL.');
      return;
    }
    await enqueueURL(url, 'context_menu');
  })();
});

ext.action.onClicked.addListener((tab) => {
  void (async () => {
    if (tab.url) {
      await enqueueURL(tab.url, 'toolbar');
      return;
    }
    await enqueueActiveTabURL('toolbar');
  })();
});

ext.commands.onCommand.addListener((command) => {
  if (command === COMMAND_IDS.openSidePanel) {
    openSidePanelInCurrentWindow();
    return;
  }
  if (command === COMMAND_IDS.enqueueCurrentPage) {
    void enqueueActiveTabURL('command');
  }
});

ext.alarms.onAlarm.addListener((alarm) => {
  if (alarm.name !== POLL_ALARM) return;
  void pollNotifications().catch((error) => {
    console.debug('Notification polling failed', error);
  });
});

ext.runtime.onMessage.addListener((message) => {
  if (message?.type === 'videofetch.poll_notifications') {
    void pollNotifications();
  }
});
