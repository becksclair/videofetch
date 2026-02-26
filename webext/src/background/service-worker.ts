import { enqueueSingle, fetchDownloads } from '@/lib/api';
import { loadSettings } from '@/lib/storage';
import type { DownloadStatus } from '@/types';

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

const lastStatusById = new Map<number, DownloadStatus>();

function notify(title: string, message: string): void {
  chrome.notifications.create({
    type: 'basic',
    iconUrl: 'icons/icon-128.png',
    title,
    message
  });
}

async function ensureMenus(): Promise<void> {
  await chrome.contextMenus.removeAll();
  chrome.contextMenus.create({
    id: MENU_IDS.link,
    title: 'Send link URL to VideoFetch',
    contexts: ['link']
  });
  chrome.contextMenus.create({
    id: MENU_IDS.media,
    title: 'Send media source to VideoFetch',
    contexts: ['audio', 'video']
  });
  chrome.contextMenus.create({
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
    await enqueueSingle(settings.serverBaseUrl, url);
    notify('VideoFetch queued', url);
    chrome.runtime.sendMessage({
      type: 'videofetch.enqueued',
      url,
      source
    });
  } catch (error) {
    notify('VideoFetch enqueue failed', String(error));
  }
}

function openSidePanelInCurrentWindow(): void {
  void chrome.sidePanel
    .open({ windowId: chrome.windows.WINDOW_ID_CURRENT })
    .catch((error) => {
      notify('VideoFetch', `Failed to open side panel: ${String(error)}`);
    });
}

async function enqueueActiveTabURL(source: 'toolbar' | 'command'): Promise<void> {
  const [tab] = await chrome.tabs.query({ active: true, lastFocusedWindow: true });
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
  return next === 'completed' || next === 'error' || next === 'canceled';
}

async function pollNotifications(): Promise<void> {
  const settings = await loadSettings();
  if (!settings.notificationsEnabled) {
    return;
  }

  const rows = await fetchDownloads(settings.serverBaseUrl, { limit: 200, offset: 0 });
  for (const row of rows) {
    const previous = lastStatusById.get(row.id);
    if (shouldNotifyTransition(row.status, previous)) {
      const title = row.title?.trim() || row.url;
      notify(`VideoFetch: ${row.status.toUpperCase()}`, `${title}`);
    }
    lastStatusById.set(row.id, row.status);
  }
}

chrome.runtime.onInstalled.addListener(() => {
  void ensureMenus();
  chrome.alarms.create(POLL_ALARM, { periodInMinutes: 0.5 });
});

chrome.runtime.onStartup.addListener(() => {
  chrome.alarms.create(POLL_ALARM, { periodInMinutes: 0.5 });
});

chrome.contextMenus.onClicked.addListener((info, tab) => {
  void (async () => {
    const url = extractContextUrl(info, tab);
    if (!url) {
      notify('VideoFetch enqueue failed', 'Selected context does not provide an http/https URL.');
      return;
    }
    await enqueueURL(url, 'context_menu');
  })();
});

chrome.action.onClicked.addListener((tab) => {
  void (async () => {
    if (tab.url) {
      await enqueueURL(tab.url, 'toolbar');
      return;
    }
    await enqueueActiveTabURL('toolbar');
  })();
});

chrome.commands.onCommand.addListener((command) => {
  if (command === COMMAND_IDS.openSidePanel) {
    openSidePanelInCurrentWindow();
    return;
  }
  if (command === COMMAND_IDS.enqueueCurrentPage) {
    void enqueueActiveTabURL('command');
  }
});

chrome.alarms.onAlarm.addListener((alarm) => {
  if (alarm.name !== POLL_ALARM) return;
  void pollNotifications().catch((error) => {
    console.debug('Notification polling failed', error);
  });
});

chrome.runtime.onMessage.addListener((message) => {
  if (message?.type === 'videofetch.poll_notifications') {
    void pollNotifications();
  }
});
