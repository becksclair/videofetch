import { DEFAULT_SETTINGS, type ExtensionSettings } from '@/types';

const SETTINGS_KEY = 'videofetch_settings_v1';

export async function loadSettings(): Promise<ExtensionSettings> {
  const result = await chrome.storage.sync.get(SETTINGS_KEY);
  const raw = result[SETTINGS_KEY] as Partial<ExtensionSettings> | undefined;
  return {
    serverBaseUrl: raw?.serverBaseUrl?.trim() || DEFAULT_SETTINGS.serverBaseUrl,
    notificationsEnabled:
      typeof raw?.notificationsEnabled === 'boolean'
        ? raw.notificationsEnabled
        : DEFAULT_SETTINGS.notificationsEnabled
  };
}

export async function saveSettings(settings: ExtensionSettings): Promise<void> {
  await chrome.storage.sync.set({
    [SETTINGS_KEY]: {
      serverBaseUrl: settings.serverBaseUrl.trim(),
      notificationsEnabled: settings.notificationsEnabled
    }
  });
}
