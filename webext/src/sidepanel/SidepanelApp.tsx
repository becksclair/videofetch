import { useEffect, useMemo, useRef, useState } from 'react';
import {
  controlAction,
  decodeWsMessage,
  deleteRecord,
  enqueueSingle,
  fetchDownloads,
  removeRecord,
  wsUrl
} from '@/lib/api';
import { loadSettings, saveSettings } from '@/lib/storage';
import type { DownloadRow, DownloadStatus, ExtensionSettings } from '@/types';

const PAGE_SIZE = 120;

type RealtimeMode = 'ws' | 'poll';
type RailTone = 'ok' | 'warn' | 'err';

const statusRank: Record<DownloadStatus, number> = {
  downloading: 0,
  pending: 1,
  paused: 2,
  error: 3,
  canceled: 4,
  completed: 5
};

const statusBadgeClass: Record<DownloadStatus, string> = {
  downloading: 'border border-cyan-400/50 bg-cyan-500/15 text-cyan-200',
  pending: 'border border-sky-400/50 bg-sky-500/15 text-sky-200',
  paused: 'border border-amber-400/50 bg-amber-500/15 text-amber-200',
  completed: 'border border-emerald-400/60 bg-emerald-500/15 text-emerald-200',
  error: 'border border-rose-400/60 bg-rose-500/15 text-rose-200',
  canceled: 'border border-zinc-500/60 bg-zinc-500/15 text-zinc-200'
};

function sortRows(rows: DownloadRow[]): DownloadRow[] {
  return [...rows].sort((a, b) => {
    const rankDiff = statusRank[a.status] - statusRank[b.status];
    if (rankDiff !== 0) return rankDiff;
    return new Date(b.updated_at).getTime() - new Date(a.updated_at).getTime();
  });
}

function titleFor(row: DownloadRow): string {
  return row.title?.trim() || row.url;
}

function formatPercent(value: number): string {
  return `${Math.max(0, Math.min(100, value)).toFixed(1)}%`;
}

function statusLabel(status: DownloadStatus): string {
  switch (status) {
    case 'pending':
      return 'QUEUED';
    case 'downloading':
      return 'LIVE';
    case 'paused':
      return 'PAUSED';
    case 'completed':
      return 'DONE';
    case 'error':
      return 'ERROR';
    case 'canceled':
      return 'CANCELED';
  }
}

export function SidepanelApp() {
  const [settings, setSettings] = useState<ExtensionSettings | null>(null);
  const [draftServerUrl, setDraftServerUrl] = useState('');
  const [mode, setMode] = useState<RealtimeMode>('poll');
  const [railMessage, setRailMessage] = useState('Initializing VideoFetch link...');
  const [railTone, setRailTone] = useState<RailTone>('warn');
  const [downloads, setDownloads] = useState<DownloadRow[]>([]);
  const [enqueueUrl, setEnqueueUrl] = useState('');
  const [enqueueBusy, setEnqueueBusy] = useState(false);
  const [actionBusy, setActionBusy] = useState<Record<number, string>>({});
  const [historyOffset, setHistoryOffset] = useState(PAGE_SIZE);
  const [historyLoading, setHistoryLoading] = useState(false);
  const reconnectTimer = useRef<number | null>(null);
  const pollTimer = useRef<number | null>(null);

  const activeRows = useMemo(
    () => downloads.filter((row) => row.status === 'pending' || row.status === 'downloading' || row.status === 'paused'),
    [downloads]
  );
  const historyRows = useMemo(
    () => downloads.filter((row) => row.status !== 'pending' && row.status !== 'downloading' && row.status !== 'paused'),
    [downloads]
  );

  useEffect(() => {
    let mounted = true;
    loadSettings()
      .then((loaded) => {
        if (!mounted) return;
        setSettings(loaded);
        setDraftServerUrl(loaded.serverBaseUrl);
      })
      .catch((error) => {
        if (!mounted) return;
        setRailTone('err');
        setRailMessage(`Failed to load settings: ${String(error)}`);
      });

    return () => {
      mounted = false;
      if (reconnectTimer.current) window.clearInterval(reconnectTimer.current);
      if (pollTimer.current) window.clearInterval(pollTimer.current);
    };
  }, []);

  useEffect(() => {
    const listener = (message: unknown) => {
      const payload = message as { type?: string; source?: string };
      if (payload?.type !== 'videofetch.enqueued' || payload.source !== 'command') {
        return;
      }
      setRailTone('ok');
      setRailMessage('Keyboard enqueue accepted for current tab URL.');
    };

    chrome.runtime.onMessage.addListener(listener);
    return () => {
      chrome.runtime.onMessage.removeListener(listener);
    };
  }, []);

  useEffect(() => {
    if (!settings) return;

    let closed = false;
    let socket: WebSocket | null = null;

    const stopPolling = () => {
      if (pollTimer.current) {
        window.clearInterval(pollTimer.current);
        pollTimer.current = null;
      }
    };

    const refreshViaHttp = async () => {
      try {
        const rows = await fetchDownloads(settings.serverBaseUrl, { limit: PAGE_SIZE, offset: 0 });
        if (closed) return;
        setDownloads(sortRows(rows));
        setHistoryOffset(PAGE_SIZE);
      } catch (error) {
        if (closed) return;
        setRailTone('err');
        setRailMessage(`Polling failed: ${String(error)}`);
      }
    };

    const startPolling = (reason: string) => {
      setMode('poll');
      setRailTone('warn');
      setRailMessage(`${reason}. Polling every second while reconnecting WebSocket...`);
      void refreshViaHttp();
      if (!pollTimer.current) {
        pollTimer.current = window.setInterval(() => {
          void refreshViaHttp();
        }, 1000);
      }
      if (!reconnectTimer.current) {
        reconnectTimer.current = window.setInterval(() => {
          if (!socket || socket.readyState === WebSocket.CLOSED) {
            connectSocket();
          }
        }, 5000);
      }
    };

    const connectSocket = () => {
      try {
        socket = new WebSocket(wsUrl(settings.serverBaseUrl, PAGE_SIZE));
      } catch (error) {
        startPolling(`WebSocket setup failed: ${String(error)}`);
        return;
      }

      socket.onopen = () => {
        if (closed) return;
        setMode('ws');
        setRailTone('ok');
        setRailMessage('Realtime stream online via WebSocket.');
        stopPolling();
        if (reconnectTimer.current) {
          window.clearInterval(reconnectTimer.current);
          reconnectTimer.current = null;
        }
      };

      socket.onmessage = (event) => {
        if (closed) return;
        const parsed = decodeWsMessage(event.data);
        if (!parsed) return;
        if (parsed.type === 'snapshot') {
          setDownloads(sortRows(parsed.downloads));
          setHistoryOffset(PAGE_SIZE);
          return;
        }
        if (parsed.type === 'diff') {
          setDownloads((current) => {
            const byID = new Map<number, DownloadRow>();
            for (const row of current) {
              byID.set(row.id, row);
            }
            for (const row of parsed.upserts) {
              byID.set(row.id, row);
            }
            for (const id of parsed.deletes) {
              byID.delete(id);
            }
            return sortRows(Array.from(byID.values()));
          });
        }
      };

      socket.onerror = () => {
        if (closed) return;
        startPolling('Realtime stream dropped');
      };

      socket.onclose = () => {
        if (closed) return;
        startPolling('WebSocket disconnected');
      };
    };

    connectSocket();

    return () => {
      closed = true;
      if (socket && (socket.readyState === WebSocket.OPEN || socket.readyState === WebSocket.CONNECTING)) {
        socket.close();
      }
      if (reconnectTimer.current) {
        window.clearInterval(reconnectTimer.current);
        reconnectTimer.current = null;
      }
      stopPolling();
    };
  }, [settings]);

  const saveServerUrl = async () => {
    if (!settings) return;
    const next = { ...settings, serverBaseUrl: draftServerUrl.trim() };
    setSettings(next);
    await saveSettings(next);
    setRailTone('warn');
    setRailMessage('Server URL saved. Reconnecting stream...');
  };

  const setBusy = (id: number, action: string | null) => {
    setActionBusy((current) => {
      const next = { ...current };
      if (!action) {
        delete next[id];
      } else {
        next[id] = action;
      }
      return next;
    });
  };

  const runAction = async (row: DownloadRow, action: 'pause' | 'resume' | 'cancel' | 'play' | 'remove' | 'delete') => {
    if (!settings) return;
    setBusy(row.id, action);
    try {
      if (action === 'remove') {
        await removeRecord(settings.serverBaseUrl, row.id);
      } else if (action === 'delete') {
        await deleteRecord(settings.serverBaseUrl, row.id);
      } else {
        await controlAction(settings.serverBaseUrl, action, row.id);
      }
      setRailTone('ok');
      setRailMessage(`${action.toUpperCase()} accepted for #${row.id}.`);
      const rows = await fetchDownloads(settings.serverBaseUrl, { limit: historyOffset, offset: 0 });
      setDownloads(sortRows(rows));
    } catch (error) {
      setRailTone('err');
      setRailMessage(`${action.toUpperCase()} failed: ${String(error)}`);
    } finally {
      setBusy(row.id, null);
    }
  };

  const submitEnqueue = async () => {
    if (!settings) return;
    if (!enqueueUrl.trim()) return;
    setEnqueueBusy(true);
    try {
      await enqueueSingle(settings.serverBaseUrl, enqueueUrl.trim());
      setEnqueueUrl('');
      setRailTone('ok');
      setRailMessage('URL accepted and routed to VideoFetch.');
    } catch (error) {
      setRailTone('err');
      setRailMessage(`Enqueue failed: ${String(error)}`);
    } finally {
      setEnqueueBusy(false);
    }
  };

  const loadMoreHistory = async () => {
    if (!settings) return;
    setHistoryLoading(true);
    try {
      const more = await fetchDownloads(settings.serverBaseUrl, {
        limit: PAGE_SIZE,
        offset: historyOffset
      });
      if (more.length === 0) {
        setRailTone('warn');
        setRailMessage('No additional history rows returned.');
      }
      setHistoryOffset((current) => current + PAGE_SIZE);
      setDownloads((current) => {
        const byId = new Map<number, DownloadRow>();
        [...current, ...more].forEach((row) => byId.set(row.id, row));
        return sortRows(Array.from(byId.values()));
      });
    } catch (error) {
      setRailTone('err');
      setRailMessage(`History fetch failed: ${String(error)}`);
    } finally {
      setHistoryLoading(false);
    }
  };

  return (
    <main className="min-h-screen p-3 text-[13px] text-slate-100">
      <section className="vf-card rounded-md p-3">
        <div className="vf-sheen rounded-md p-3">
          <div className="mb-2 flex items-center justify-between gap-3">
            <h1 className="text-[11px] font-bold tracking-[0.22em] text-emerald-200">VIDEOFETCH CONTROL RAIL</h1>
            <div className="rounded border border-emerald-400/40 bg-black/40 px-2 py-1 text-[10px] tracking-[0.18em] text-emerald-100">
              {mode === 'ws' ? 'REALTIME WS' : 'DEGRADED POLL'}
            </div>
          </div>

          <div className={`mb-3 rounded bg-black/35 px-3 py-2 text-[11px] ${railTone === 'ok' ? 'vf-rail-ok' : railTone === 'warn' ? 'vf-rail-warn' : 'vf-rail-err'}`}>
            {railMessage}
          </div>

          <div className="grid gap-2">
            <label className="text-[10px] uppercase tracking-[0.18em] text-slate-300">VideoFetch Server URL</label>
            <div className="flex gap-2">
              <input
                value={draftServerUrl}
                onChange={(event) => setDraftServerUrl(event.target.value)}
                className="w-full rounded border border-slate-600 bg-black/55 px-2 py-1.5 text-slate-100 outline-none focus:border-emerald-400"
                placeholder="http://127.0.0.1:8080"
              />
              <button
                className="rounded border border-emerald-400/40 bg-emerald-500/15 px-3 py-1.5 text-[11px] font-semibold text-emerald-100 hover:bg-emerald-500/25"
                onClick={saveServerUrl}
                type="button"
              >
                APPLY
              </button>
            </div>
          </div>
        </div>
      </section>

      <section className="mt-3 vf-card rounded-md p-3">
        <div className="mb-2 text-[10px] uppercase tracking-[0.18em] text-slate-300">Queue URL</div>
        <div className="flex gap-2">
          <input
            className="w-full rounded border border-slate-700 bg-black/55 px-2 py-1.5 outline-none focus:border-cyan-400"
            placeholder="https://example.com/video"
            value={enqueueUrl}
            onChange={(event) => setEnqueueUrl(event.target.value)}
            onKeyDown={(event) => {
              if (event.key === 'Enter') {
                event.preventDefault();
                void submitEnqueue();
              }
            }}
          />
          <button
            className="rounded border border-cyan-400/40 bg-cyan-400/15 px-3 py-1.5 text-[11px] font-semibold text-cyan-100 hover:bg-cyan-400/25 disabled:opacity-45"
            onClick={() => void submitEnqueue()}
            type="button"
            disabled={enqueueBusy}
          >
            {enqueueBusy ? 'QUEUEING' : 'ENQUEUE'}
          </button>
        </div>
      </section>

      <DownloadSection title="ACTIVE" rows={activeRows} busyMap={actionBusy} onAction={runAction} />
      <DownloadSection title="RECENT HISTORY" rows={historyRows} busyMap={actionBusy} onAction={runAction} />

      <div className="mt-3">
        <button
          onClick={() => void loadMoreHistory()}
          disabled={historyLoading}
          className="w-full rounded border border-slate-600 bg-black/45 px-3 py-2 text-[11px] tracking-[0.14em] text-slate-200 hover:border-slate-400 disabled:opacity-45"
          type="button"
        >
          {historyLoading ? 'LOADING...' : 'LOAD MORE HISTORY'}
        </button>
      </div>
    </main>
  );
}

function DownloadSection({
  title,
  rows,
  busyMap,
  onAction
}: {
  title: string;
  rows: DownloadRow[];
  busyMap: Record<number, string>;
  onAction: (row: DownloadRow, action: 'pause' | 'resume' | 'cancel' | 'play' | 'remove' | 'delete') => Promise<void>;
}) {
  return (
    <section className="mt-3 vf-card rounded-md p-3">
      <header className="mb-2 flex items-center justify-between">
        <h2 className="text-[10px] uppercase tracking-[0.2em] text-slate-300">{title}</h2>
        <span className="rounded border border-slate-600 bg-black/40 px-2 py-0.5 text-[10px] text-slate-300">{rows.length}</span>
      </header>

      {rows.length === 0 ? (
        <div className="rounded border border-dashed border-slate-700 bg-black/35 px-3 py-4 text-[11px] text-slate-400">No rows.</div>
      ) : (
        <ul className="space-y-2">
          {rows.map((row) => {
            const busyAction = busyMap[row.id];
            return (
              <li key={row.id} className="rounded border border-slate-700 bg-black/40 p-2">
                <div className="mb-1 flex items-center justify-between gap-2">
                  <span className={`rounded px-2 py-0.5 text-[10px] font-semibold tracking-[0.12em] ${statusBadgeClass[row.status]}`}>
                    {statusLabel(row.status)}
                  </span>
                  <span className="text-[10px] text-slate-400">#{row.id}</span>
                </div>

                <div className="line-clamp-2 text-[11px] text-slate-100" title={titleFor(row)}>
                  {titleFor(row)}
                </div>

                <div className="mt-2 h-2 overflow-hidden rounded bg-slate-800">
                  <div
                    className="h-full rounded bg-gradient-to-r from-emerald-400 to-cyan-300 transition-[width] duration-300"
                    style={{ width: `${Math.max(0, Math.min(100, row.progress))}%` }}
                  />
                </div>

                <div className="mt-1 text-[10px] text-slate-400">{formatPercent(row.progress)}</div>

                {row.error_message ? (
                  <div className="mt-1 rounded border border-rose-400/30 bg-rose-400/10 px-2 py-1 text-[10px] text-rose-100" title={row.error_message}>
                    {row.error_message.length > 140 ? `${row.error_message.slice(0, 140)}…` : row.error_message}
                  </div>
                ) : null}

                <div className="mt-2 flex flex-wrap gap-1">
                  {controlsFor(row).map((control) => (
                    <button
                      key={`${row.id}-${control.action}`}
                      onClick={() => void onAction(row, control.action)}
                      disabled={Boolean(busyAction)}
                      className={`rounded px-2 py-1 text-[10px] font-semibold tracking-[0.08em] ${control.className} disabled:opacity-40`}
                      type="button"
                    >
                      {busyAction === control.action ? 'WORKING' : control.label}
                    </button>
                  ))}
                </div>
              </li>
            );
          })}
        </ul>
      )}
    </section>
  );
}

function controlsFor(row: DownloadRow): Array<{ action: 'pause' | 'resume' | 'cancel' | 'play' | 'remove' | 'delete'; label: string; className: string }> {
  const neutral = 'border border-slate-600 bg-slate-700/35 text-slate-100 hover:border-slate-300';
  const warn = 'border border-amber-400/45 bg-amber-500/15 text-amber-100 hover:bg-amber-500/25';
  const danger = 'border border-rose-400/45 bg-rose-500/15 text-rose-100 hover:bg-rose-500/25';
  const ok = 'border border-emerald-400/45 bg-emerald-500/15 text-emerald-100 hover:bg-emerald-500/25';

  switch (row.status) {
    case 'pending':
      return [
        { action: 'pause', label: 'PAUSE', className: warn },
        { action: 'cancel', label: 'CANCEL', className: danger }
      ];
    case 'downloading':
      return [
        { action: 'pause', label: 'PAUSE', className: warn },
        { action: 'cancel', label: 'CANCEL', className: danger }
      ];
    case 'paused':
      return [
        { action: 'resume', label: 'RESUME', className: ok },
        { action: 'cancel', label: 'CANCEL', className: danger }
      ];
    case 'error':
    case 'canceled':
      return [
        { action: 'resume', label: 'RESUME', className: ok },
        { action: 'remove', label: 'REMOVE', className: neutral }
      ];
    case 'completed':
      return [
        { action: 'play', label: 'PLAY', className: ok },
        { action: 'delete', label: 'DELETE', className: danger },
        { action: 'remove', label: 'REMOVE', className: neutral }
      ];
  }
}
