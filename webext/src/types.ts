export type DownloadStatus =
  | 'pending'
  | 'downloading'
  | 'paused'
  | 'completed'
  | 'error'
  | 'canceled';

export interface DownloadRow {
  id: number;
  url: string;
  title: string;
  duration: number;
  thumbnail_url: string;
  status: DownloadStatus;
  progress: number;
  filename: string;
  error_message?: string;
  created_at: string;
  updated_at: string;
}

export interface DownloadsResponse {
  status: 'success' | 'error';
  downloads: DownloadRow[];
  message?: string;
}

export interface ExtensionSettings {
  serverBaseUrl: string;
  notificationsEnabled: boolean;
}

export const DEFAULT_SETTINGS: ExtensionSettings = {
  serverBaseUrl: 'http://127.0.0.1:8080',
  notificationsEnabled: true
};
