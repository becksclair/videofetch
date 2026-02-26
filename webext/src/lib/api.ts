import type { DownloadRow, DownloadsResponse } from '@/types';

export function normalizeBaseUrl(input: string): string {
  return input.replace(/\/+$/, '');
}

function endpoint(baseUrl: string, path: string): string {
  return `${normalizeBaseUrl(baseUrl)}${path}`;
}

async function requestJson<T>(url: string, init?: RequestInit): Promise<T> {
  const response = await fetch(url, {
    ...init,
    headers: {
      'Content-Type': 'application/json',
      ...(init?.headers || {})
    }
  });
  const payload = (await response.json()) as T & { message?: string; status?: string };
  if (!response.ok) {
    throw new Error(payload.message || `Request failed (${response.status})`);
  }
  return payload;
}

export async function fetchDownloads(
  baseUrl: string,
  options: { limit?: number; offset?: number; status?: string } = {}
): Promise<DownloadRow[]> {
  const query = new URLSearchParams();
  if (options.limit && options.limit > 0) query.set('limit', String(options.limit));
  if (options.offset && options.offset > 0) query.set('offset', String(options.offset));
  if (options.status) query.set('status', options.status);
  const suffix = query.toString() ? `?${query.toString()}` : '';
  const data = await requestJson<DownloadsResponse>(endpoint(baseUrl, `/api/downloads${suffix}`));
  return data.downloads || [];
}

export async function enqueueSingle(baseUrl: string, url: string): Promise<void> {
  await requestJson(endpoint(baseUrl, '/api/download_single'), {
    method: 'POST',
    body: JSON.stringify({ url })
  });
}

export async function removeRecord(baseUrl: string, id: number): Promise<void> {
  await requestJson(endpoint(baseUrl, '/api/remove'), {
    method: 'DELETE',
    body: JSON.stringify({ id })
  });
}

export async function deleteRecord(baseUrl: string, id: number): Promise<void> {
  await requestJson(endpoint(baseUrl, '/api/delete'), {
    method: 'DELETE',
    body: JSON.stringify({ id })
  });
}

export async function controlAction(
  baseUrl: string,
  action: 'pause' | 'resume' | 'cancel' | 'play',
  id: number
): Promise<void> {
  await requestJson(endpoint(baseUrl, `/api/control/${action}`), {
    method: 'POST',
    body: JSON.stringify({ id })
  });
}

export function wsUrl(baseUrl: string, limit = 200): string {
  const normalized = normalizeBaseUrl(baseUrl);
  const parsed = new URL(normalized);
  parsed.protocol = parsed.protocol === 'https:' ? 'wss:' : 'ws:';
  parsed.pathname = '/api/ws/downloads';
  parsed.searchParams.set('limit', String(limit));
  return parsed.toString();
}

export type WsSnapshot = {
  type: 'snapshot';
  downloads: DownloadRow[];
  at: string;
};

export type WsDiff = {
  type: 'diff';
  upserts: DownloadRow[];
  deletes: number[];
  at: string;
};

export type WsHeartbeat = {
  type: 'heartbeat';
  at: string;
};

export type WsMessage = WsSnapshot | WsDiff | WsHeartbeat;

export function decodeWsMessage(raw: string): WsMessage | null {
  try {
    const data = JSON.parse(raw) as WsMessage;
    if (data.type === 'snapshot' && Array.isArray(data.downloads)) {
      return data;
    }
    if (data.type === 'diff' && Array.isArray(data.upserts) && Array.isArray(data.deletes)) {
      return data;
    }
    if (data.type === 'heartbeat') {
      return data;
    }
  } catch {
    return null;
  }
  return null;
}
