import { type MouseEvent, useCallback, useEffect, useMemo, useRef, useState } from 'react';
import {
  clearHistory,
  controlAction,
  decodeWsMessage,
  deleteRecord,
  enqueueSingle,
  fetchDownloads,
  isAuthRequiredError,
  normalizeBaseUrl,
  removeRecord,
  wsUrl
} from '@/lib/api';
import { loadSettings } from '@/lib/storage';
import { ext, hasServerPermission } from '@/lib/webext';
import type { DownloadRow, DownloadStatus, ExtensionSettings } from '@/types';

const PAGE_SIZE = 120;
const DOWNLOAD_LIST_OPTIONS = {
  sort: 'updated_at',
  order: 'desc'
} as const;
const ACTIVE_DOWNLOAD_LIST_OPTIONS = {
  ...DOWNLOAD_LIST_OPTIONS,
  status: 'active'
} as const;
const HISTORY_DOWNLOAD_LIST_OPTIONS = {
  ...DOWNLOAD_LIST_OPTIONS,
  status: 'history'
} as const;

type RealtimeMode = 'ws' | 'poll';
type RailTone = 'ok' | 'warn' | 'err';
type RowAction = 'pause' | 'resume' | 'cancel' | 'play' | 'remove' | 'delete';
type RowControl = {
  action: RowAction;
  label: string;
  className: string;
  slot: 'primary' | 'secondary';
};

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

function queueCandidateURL(url: string | undefined): string {
  if (!url) {
    return '';
  }
  return /^https?:\/\//i.test(url) ? url : '';
}

async function fetchVisibleDownloads(baseUrl: string, historyLimit: number): Promise<DownloadRow[]> {
  const [activeRows, historyRows] = await Promise.all([
    fetchDownloads(baseUrl, { ...ACTIVE_DOWNLOAD_LIST_OPTIONS, limit: PAGE_SIZE, offset: 0 }),
    fetchDownloads(baseUrl, { ...HISTORY_DOWNLOAD_LIST_OPTIONS, limit: historyLimit, offset: 0 })
  ]);
  return mergeRows(activeRows, historyRows);
}

function mergeRows(...groups: DownloadRow[][]): DownloadRow[] {
  const byID = new Map<number, DownloadRow>();
  for (const group of groups) {
    for (const row of group) {
      byID.set(row.id, row);
    }
  }
  return sortRows(Array.from(byID.values()));
}

function applyRowsUpserts(current: DownloadRow[], upserts: DownloadRow[]): DownloadRow[] {
  const byID = new Map<number, DownloadRow>();
  for (const row of current) {
    byID.set(row.id, row);
  }
  for (const row of upserts) {
    byID.set(row.id, row);
  }
  return sortRows(Array.from(byID.values()));
}

export function SidepanelApp() {
  const [settings, setSettings] = useState<ExtensionSettings | null>(null);
  const [mode, setMode] = useState<RealtimeMode>('poll');
  const [railMessage, setRailMessage] = useState('Initializing VideoFetch link...');
  const [railTone, setRailTone] = useState<RailTone>('warn');
  const [needsServerPermission, setNeedsServerPermission] = useState(false);
  const [needsAuth, setNeedsAuth] = useState(false);
  const [retryNonce, setRetryNonce] = useState(0);
  const [downloads, setDownloads] = useState<DownloadRow[]>([]);
  const [enqueueUrl, setEnqueueUrl] = useState('');
  const [enqueueBusy, setEnqueueBusy] = useState(false);
  const [clearHistoryBusy, setClearHistoryBusy] = useState(false);
  const [showClearHistoryConfirm, setShowClearHistoryConfirm] = useState(false);
  const [actionBusy, setActionBusy] = useState<Record<number, string>>({});
  const [historyOffset, setHistoryOffset] = useState(PAGE_SIZE);
  const [historyLoading, setHistoryLoading] = useState(false);
  const [copiedByID, setCopiedByID] = useState<Record<number, boolean>>({});
  const reconnectTimer = useRef<number | null>(null);
  const pollTimer = useRef<number | null>(null);
  const enqueueSyncSeq = useRef(0);
  const httpRefreshSeq = useRef(0);
  const copyResetTimers = useRef<Record<number, number>>({});
  const historyOffsetRef = useRef(PAGE_SIZE);
  const wsUpsertSeq = useRef(0);
  const wsUpsertsByID = useRef(new Map<number, { row: DownloadRow; seq: number }>());
  const needsAuthRef = useRef(false);
  const enterAuthRequiredRef = useRef<(() => void) | null>(null);

  const activeRows = useMemo(
    () => downloads.filter((row) => row.status === 'pending' || row.status === 'downloading' || row.status === 'paused'),
    [downloads]
  );
  const historyRows = useMemo(
    () => downloads.filter((row) => row.status !== 'pending' && row.status !== 'downloading' && row.status !== 'paused'),
    [downloads]
  );

  useEffect(() => {
    historyOffsetRef.current = historyOffset;
  }, [historyOffset]);

  const syncEnqueueFromActiveTab = useCallback(async () => {
    const requestID = ++enqueueSyncSeq.current;
    try {
      const [tab] = await ext.tabs.query({ active: true, lastFocusedWindow: true });
      if (requestID !== enqueueSyncSeq.current) {
        return;
      }
      setEnqueueUrl(queueCandidateURL(tab?.url));
    } catch {
      if (requestID !== enqueueSyncSeq.current) {
        return;
      }
      setEnqueueUrl('');
    }
  }, []);

  useEffect(() => {
    let mounted = true;
    loadSettings()
      .then((loaded) => {
        if (!mounted) return;
        void hasServerPermission(loaded.serverBaseUrl)
          .then((hasPermission) => {
            if (!mounted) return;
            setNeedsServerPermission(!hasPermission);
            if (!hasPermission) {
              setSettings(null);
              setRailTone('warn');
              setRailMessage('Server access permission is required. Open settings and save the VideoFetch Base URL.');
              return;
            }
            setSettings(loaded);
            setRailTone('warn');
            setRailMessage('VideoFetch link ready. Connecting realtime stream...');
          })
          .catch((error) => {
            if (!mounted) return;
            setSettings(null);
            setRailTone('err');
            setRailMessage(`Failed to check server permission: ${String(error)}`);
          });
      })
      .catch((error) => {
        if (!mounted) return;
        setRailTone('err');
        setRailMessage(`Failed to load settings: ${String(error)}`);
      });

    return () => {
      mounted = false;
      for (const timerID of Object.values(copyResetTimers.current)) {
        window.clearTimeout(timerID);
      }
      copyResetTimers.current = {};
      if (reconnectTimer.current) window.clearInterval(reconnectTimer.current);
      if (pollTimer.current) window.clearInterval(pollTimer.current);
    };
  }, []);

  useEffect(() => {
    let active = true;
    const runSync = () => {
      if (!active) {
        return;
      }
      void syncEnqueueFromActiveTab();
    };

    const onActivated: Parameters<typeof ext.tabs.onActivated.addListener>[0] = () => {
      runSync();
    };

    const onUpdated: Parameters<typeof ext.tabs.onUpdated.addListener>[0] = (_tabID, changeInfo, tab) => {
      if (!tab.active || !changeInfo.url) {
        return;
      }
      runSync();
    };

    const onFocusChanged: Parameters<typeof ext.windows.onFocusChanged.addListener>[0] = (windowID) => {
      if (windowID === ext.windows.WINDOW_ID_NONE) {
        return;
      }
      runSync();
    };

    runSync();
    ext.tabs.onActivated.addListener(onActivated);
    ext.tabs.onUpdated.addListener(onUpdated);
    ext.windows.onFocusChanged.addListener(onFocusChanged);

    return () => {
      active = false;
      ext.tabs.onActivated.removeListener(onActivated);
      ext.tabs.onUpdated.removeListener(onUpdated);
      ext.windows.onFocusChanged.removeListener(onFocusChanged);
    };
  }, [syncEnqueueFromActiveTab]);

  const openSignIn = useCallback(() => {
    if (!settings) return;
    void ext.tabs.create({ url: normalizeBaseUrl(settings.serverBaseUrl) });
  }, [settings]);

  const retryConnection = useCallback(() => {
    needsAuthRef.current = false;
    setNeedsAuth(false);
    setRailTone('warn');
    setRailMessage('Reconnecting to VideoFetch...');
    setRetryNonce((current) => current + 1);
  }, []);

  const resumeConnectionIfAuthLatched = useCallback(() => {
    if (needsAuthRef.current) {
      retryConnection();
    }
  }, [retryConnection]);

  useEffect(() => {
    const listener = (message: unknown) => {
      const payload = message as { type?: string; source?: string };
      if (payload?.type !== 'videofetch.enqueued') {
        return;
      }
      if (payload.source === 'command') {
        setRailTone('ok');
        setRailMessage('Keyboard enqueue accepted for current tab URL.');
      }
      resumeConnectionIfAuthLatched();
    };

    ext.runtime.onMessage.addListener(listener);
    return () => {
      ext.runtime.onMessage.removeListener(listener);
    };
  }, [resumeConnectionIfAuthLatched]);

  useEffect(() => {
    if (!needsAuth || !settings) return;

    let serverOrigin: string;
    try {
      serverOrigin = new URL(settings.serverBaseUrl).origin;
    } catch {
      return;
    }

    const onUpdated: Parameters<typeof ext.tabs.onUpdated.addListener>[0] = (_tabID, changeInfo, tab) => {
      if (changeInfo.status !== 'complete' || !tab.url) {
        return;
      }
      try {
        if (new URL(tab.url).origin === serverOrigin) {
          retryConnection();
        }
      } catch {
        // ignore non-URL tab values
      }
    };

    ext.tabs.onUpdated.addListener(onUpdated);
    return () => {
      ext.tabs.onUpdated.removeListener(onUpdated);
    };
  }, [needsAuth, settings, retryConnection]);

  useEffect(() => {
    if (!settings) return;

    let closed = false;
    let authRequired = false;
    let httpRefreshInFlight = false;
    let socket: WebSocket | null = null;

    const stopPolling = () => {
      if (pollTimer.current) {
        window.clearInterval(pollTimer.current);
        pollTimer.current = null;
      }
    };

    const stopReconnect = () => {
      if (reconnectTimer.current) {
        window.clearInterval(reconnectTimer.current);
        reconnectTimer.current = null;
      }
    };

    const enterAuthRequired = () => {
      authRequired = true;
      stopPolling();
      stopReconnect();
      if (socket && (socket.readyState === WebSocket.OPEN || socket.readyState === WebSocket.CONNECTING)) {
        socket.close();
      }
      setMode('poll');
      needsAuthRef.current = true;
      setNeedsAuth(true);
      setRailTone('warn');
      setRailMessage('Sign-in required for VideoFetch. Open sign-in to refresh your Cloudflare Access session.');
    };
    enterAuthRequiredRef.current = enterAuthRequired;

    const refreshViaHttp = async (historyLimit = historyOffsetRef.current, resetHistory = false) => {
      const requestID = ++httpRefreshSeq.current;
      const startedAtUpsertSeq = wsUpsertSeq.current;
      httpRefreshInFlight = true;
      try {
        const rows = await fetchVisibleDownloads(settings.serverBaseUrl, historyLimit);
        if (closed || authRequired || requestID !== httpRefreshSeq.current) return;
        needsAuthRef.current = false;
        setNeedsAuth(false);
        const newerUpserts = Array.from(wsUpsertsByID.current.values())
          .filter((entry) => entry.seq > startedAtUpsertSeq)
          .map((entry) => entry.row);
        setDownloads(newerUpserts.length > 0 ? mergeRows(rows, newerUpserts) : rows);
        if (resetHistory) {
          setHistoryOffset(PAGE_SIZE);
        }
      } catch (error) {
        if (closed || authRequired) return;
        if (isAuthRequiredError(error)) {
          enterAuthRequired();
          return;
        }
        if (requestID !== httpRefreshSeq.current) return;
        setRailTone('err');
        setRailMessage(`Polling failed: ${String(error)}`);
      } finally {
        if (requestID === httpRefreshSeq.current) {
          httpRefreshInFlight = false;
        }
      }
    };

    const startPolling = (reason: string) => {
      if (closed || authRequired) return;
      setMode('poll');
      setRailTone('warn');
      setRailMessage(`${reason}. Polling every second while reconnecting WebSocket...`);
      if (!httpRefreshInFlight) {
        void refreshViaHttp();
      }
      if (!pollTimer.current) {
        pollTimer.current = window.setInterval(() => {
          if (!httpRefreshInFlight) {
            void refreshViaHttp();
          }
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
      if (closed || authRequired) return;
      try {
        socket = new WebSocket(wsUrl(settings.serverBaseUrl, PAGE_SIZE));
      } catch (error) {
        startPolling(`WebSocket setup failed: ${String(error)}`);
        return;
      }

      socket.onopen = () => {
        if (closed || authRequired) return;
        setMode('ws');
        needsAuthRef.current = false;
        setNeedsAuth(false);
        stopPolling();
        setRailTone('ok');
        setRailMessage('Realtime stream online.');
        stopReconnect();
      };

      socket.onmessage = (event) => {
        if (closed || authRequired) return;
        const parsed = decodeWsMessage(event.data);
        if (!parsed) return;
        if (parsed.type === 'snapshot') {
          void refreshViaHttp(PAGE_SIZE, true);
          return;
        }
        if (parsed.type === 'diff') {
          if (parsed.upserts.length > 0) {
            const seq = wsUpsertSeq.current + 1;
            wsUpsertSeq.current = seq;
            for (const row of parsed.upserts) {
              wsUpsertsByID.current.set(row.id, { row, seq });
            }
            setDownloads((current) => applyRowsUpserts(current, parsed.upserts));
          }
          if (parsed.deletes.length > 0) {
            void refreshViaHttp();
          }
        }
      };

      socket.onerror = () => {
        if (closed || authRequired) return;
        startPolling('Realtime stream dropped');
      };

      socket.onclose = () => {
        if (closed || authRequired) return;
        startPolling('WebSocket disconnected');
      };
    };

    connectSocket();

    return () => {
      closed = true;
      if (enterAuthRequiredRef.current === enterAuthRequired) {
        enterAuthRequiredRef.current = null;
      }
      if (socket && (socket.readyState === WebSocket.OPEN || socket.readyState === WebSocket.CONNECTING)) {
        socket.close();
      }
      stopReconnect();
      stopPolling();
    };
  }, [settings, retryNonce]);

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

  const handleActionAuthError = (error: unknown): boolean => {
    if (!isAuthRequiredError(error)) {
      return false;
    }
    if (enterAuthRequiredRef.current) {
      enterAuthRequiredRef.current();
    } else {
      needsAuthRef.current = true;
      setNeedsAuth(true);
      setRailTone('warn');
      setRailMessage('Sign-in required for VideoFetch. Open sign-in to refresh your Cloudflare Access session.');
    }
    return true;
  };

  const runAction = async (row: DownloadRow, action: RowAction) => {
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
      resumeConnectionIfAuthLatched();
      try {
        const rows = await fetchVisibleDownloads(settings.serverBaseUrl, historyOffset);
        setDownloads(rows);
      } catch (refreshError) {
        if (!handleActionAuthError(refreshError)) {
          setRailTone('warn');
          setRailMessage(`${action.toUpperCase()} accepted for #${row.id}, but refresh failed: ${String(refreshError)}`);
        }
      }
    } catch (error) {
      if (!handleActionAuthError(error)) {
        setRailTone('err');
        setRailMessage(`${action.toUpperCase()} failed: ${String(error)}`);
      }
    } finally {
      setBusy(row.id, null);
    }
  };

  const copyRowURL = async (row: DownloadRow) => {
    try {
      if (!navigator.clipboard?.writeText) {
        throw new Error('clipboard_unavailable');
      }
      await navigator.clipboard.writeText(row.url);
      setCopiedByID((current) => ({ ...current, [row.id]: true }));
      const existingTimer = copyResetTimers.current[row.id];
      if (existingTimer) {
        window.clearTimeout(existingTimer);
      }
      copyResetTimers.current[row.id] = window.setTimeout(() => {
        setCopiedByID((current) => {
          const next = { ...current };
          delete next[row.id];
          return next;
        });
        delete copyResetTimers.current[row.id];
      }, 1400);
      setRailTone('ok');
      setRailMessage(`Copied URL for #${row.id}.`);
    } catch (error) {
      setRailTone('err');
      setRailMessage(`Copy failed: ${String(error)}`);
    }
  };

  const submitEnqueue = async () => {
    if (!settings) return;
    if (!enqueueUrl.trim()) return;
    setEnqueueBusy(true);
    try {
      await enqueueSingle(settings.serverBaseUrl, enqueueUrl.trim());
      await syncEnqueueFromActiveTab();
      setRailTone('ok');
      setRailMessage('URL accepted and routed to VideoFetch.');
      resumeConnectionIfAuthLatched();
    } catch (error) {
      if (!handleActionAuthError(error)) {
        setRailTone('err');
        setRailMessage(`Enqueue failed: ${String(error)}`);
      }
    } finally {
      setEnqueueBusy(false);
    }
  };

  const clearRecentHistory = async () => {
    if (!settings || clearHistoryBusy) return;
    if (historyRows.length === 0) {
      setRailTone('warn');
      setRailMessage('No recent history rows to clear.');
      return;
    }
    setShowClearHistoryConfirm(false);
    setClearHistoryBusy(true);
    try {
      const cleared = await clearHistory(settings.serverBaseUrl);
      setRailTone('ok');
      setRailMessage(`Cleared ${cleared} recent history ${cleared === 1 ? 'row' : 'rows'}.`);
      resumeConnectionIfAuthLatched();
      try {
        const rows = await fetchVisibleDownloads(settings.serverBaseUrl, historyOffset);
        setDownloads(rows);
      } catch (refreshError) {
        if (!handleActionAuthError(refreshError)) {
          setRailTone('warn');
          setRailMessage(`Cleared ${cleared} recent history ${cleared === 1 ? 'row' : 'rows'}, but refresh failed: ${String(refreshError)}`);
        }
      }
    } catch (error) {
      if (!handleActionAuthError(error)) {
        setRailTone('err');
        setRailMessage(`Clear history failed: ${String(error)}`);
      }
    } finally {
      setClearHistoryBusy(false);
    }
  };

  const requestClearRecentHistory = () => {
    if (clearHistoryBusy) return;
    if (historyRows.length === 0) {
      setRailTone('warn');
      setRailMessage('No recent history rows to clear.');
      return;
    }
    setShowClearHistoryConfirm(true);
  };

  const dismissClearHistoryConfirm = useCallback(() => {
    if (clearHistoryBusy) {
      return;
    }
    setShowClearHistoryConfirm(false);
  }, [clearHistoryBusy]);

  const handleClearHistoryBackdropClick = (event: MouseEvent<HTMLDivElement>) => {
    if (event.target !== event.currentTarget) {
      return;
    }
    dismissClearHistoryConfirm();
  };

  useEffect(() => {
    if (!showClearHistoryConfirm) {
      return;
    }
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key !== 'Escape') {
        return;
      }
      dismissClearHistoryConfirm();
    };
    window.addEventListener('keydown', onKeyDown);
    return () => {
      window.removeEventListener('keydown', onKeyDown);
    };
  }, [showClearHistoryConfirm, dismissClearHistoryConfirm]);

  const loadMoreHistory = async () => {
    if (!settings) return;
    setHistoryLoading(true);
    try {
      const more = await fetchDownloads(settings.serverBaseUrl, {
        ...HISTORY_DOWNLOAD_LIST_OPTIONS,
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
      resumeConnectionIfAuthLatched();
    } catch (error) {
      if (!handleActionAuthError(error)) {
        setRailTone('err');
        setRailMessage(`History fetch failed: ${String(error)}`);
      }
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
              {needsAuth ? 'AUTH REQUIRED' : mode === 'ws' ? 'REALTIME WS' : 'DEGRADED POLL'}
            </div>
          </div>

          <div className={`mb-3 rounded bg-black/35 px-3 py-2 text-[11px] ${railTone === 'ok' ? 'vf-rail-ok' : railTone === 'warn' ? 'vf-rail-warn' : 'vf-rail-err'}`}>
            <div>{railMessage}</div>
            {needsServerPermission ? (
              <button
                className="mt-2 rounded border border-amber-300/45 bg-amber-400/15 px-2 py-1 text-[10px] font-semibold tracking-[0.08em] text-amber-100 hover:bg-amber-400/25"
                onClick={() => void ext.runtime.openOptionsPage()}
                type="button"
              >
                OPEN SETTINGS
              </button>
            ) : null}
            {needsAuth && settings ? (
              <div className="mt-2 flex gap-2">
                <button
                  className="rounded border border-amber-300/45 bg-amber-400/15 px-2 py-1 text-[10px] font-semibold tracking-[0.08em] text-amber-100 hover:bg-amber-400/25"
                  onClick={openSignIn}
                  type="button"
                >
                  OPEN SIGN-IN
                </button>
                <button
                  className="rounded border border-slate-500/50 bg-slate-500/20 px-2 py-1 text-[10px] font-semibold tracking-[0.08em] text-slate-100 hover:bg-slate-500/30"
                  onClick={retryConnection}
                  type="button"
                >
                  RETRY
                </button>
              </div>
            ) : null}
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

      <DownloadSection title="ACTIVE" rows={activeRows} busyMap={actionBusy} copiedByID={copiedByID} onAction={runAction} onCopyUrl={copyRowURL} />
      <DownloadSection
        title="RECENT HISTORY"
        rows={historyRows}
        busyMap={actionBusy}
        copiedByID={copiedByID}
        onAction={runAction}
        onCopyUrl={copyRowURL}
        headerAction={{
          label: 'CLEAR HISTORY',
          busyLabel: 'CLEARING...',
          onClick: requestClearRecentHistory,
          busy: clearHistoryBusy,
          disabled: historyRows.length === 0
        }}
      />

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

      {showClearHistoryConfirm ? (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/75 p-3" onClick={handleClearHistoryBackdropClick}>
          <div className="w-full max-w-[360px] rounded border border-slate-600 bg-slate-900 p-3 shadow-xl" role="dialog" aria-modal="true" aria-labelledby="clear-history-title">
            <h3 id="clear-history-title" className="text-[12px] font-semibold uppercase tracking-[0.16em] text-rose-200">
              Clear recent history?
            </h3>
            <p className="mt-2 text-[11px] text-slate-200">
              This removes completed, errored, and canceled rows from recent history. Downloaded files are kept.
            </p>
            <div className="mt-3 flex items-center justify-end gap-2">
              <button
                onClick={dismissClearHistoryConfirm}
                disabled={clearHistoryBusy}
                className="rounded border border-slate-600 bg-slate-700/35 px-3 py-1.5 text-[10px] font-semibold tracking-[0.08em] text-slate-100 hover:border-slate-300 disabled:opacity-40"
                type="button"
              >
                CANCEL
              </button>
              <button
                onClick={() => void clearRecentHistory()}
                disabled={clearHistoryBusy || !settings}
                className="rounded border border-rose-400/45 bg-rose-500/15 px-3 py-1.5 text-[10px] font-semibold tracking-[0.08em] text-rose-100 hover:bg-rose-500/25 disabled:opacity-40"
                type="button"
              >
                {clearHistoryBusy ? 'CLEARING...' : 'CLEAR HISTORY'}
              </button>
            </div>
          </div>
        </div>
      ) : null}
    </main>
  );
}

function DownloadSection({
  title,
  rows,
  busyMap,
  copiedByID,
  onAction,
  onCopyUrl,
  headerAction
}: {
  title: string;
  rows: DownloadRow[];
  busyMap: Record<number, string>;
  copiedByID: Record<number, boolean>;
  onAction: (row: DownloadRow, action: RowAction) => Promise<void>;
  onCopyUrl: (row: DownloadRow) => Promise<void>;
  headerAction?: {
    label: string;
    busyLabel: string;
    onClick: () => void;
    busy: boolean;
    disabled?: boolean;
  };
}) {
  return (
    <section className="mt-3 vf-card rounded-md p-3">
      <header className="mb-2 flex items-center justify-between">
        <h2 className="text-[10px] uppercase tracking-[0.2em] text-slate-300">{title}</h2>
        <div className="flex items-center gap-1">
          <span className="rounded border border-slate-600 bg-black/40 px-2 py-0.5 text-[10px] text-slate-300">{rows.length}</span>
          {headerAction ? (
            <button
              onClick={headerAction.onClick}
              disabled={headerAction.busy || headerAction.disabled}
              className="rounded border border-slate-600 bg-slate-700/35 px-2 py-1 text-[10px] font-semibold tracking-[0.08em] text-slate-100 hover:border-slate-300 disabled:opacity-40"
              type="button"
            >
              {headerAction.busy ? headerAction.busyLabel : headerAction.label}
            </button>
          ) : null}
        </div>
      </header>

      {rows.length === 0 ? (
        <div className="rounded border border-dashed border-slate-700 bg-black/35 px-3 py-4 text-[11px] text-slate-400">No rows.</div>
      ) : (
        <ul className="space-y-2">
          {rows.map((row) => {
            const busyAction = busyMap[row.id];
            const controls = controlsFor(row);
            const primaryControls = controls.filter((control) => control.slot === 'primary');
            const secondaryControls = controls.filter((control) => control.slot === 'secondary');
            return (
              <li key={row.id} className="rounded border border-slate-700 bg-black/40 p-2">
                <div className="mb-1 flex items-center justify-between gap-2">
                  <div className="flex items-center gap-1">
                    <span className={`rounded px-2 py-0.5 text-[10px] font-semibold tracking-[0.12em] ${statusBadgeClass[row.status]}`}>
                      {statusLabel(row.status)}
                    </span>
                    <button
                      onClick={() => void onCopyUrl(row)}
                      className="rounded border border-slate-600 bg-slate-700/35 px-2 py-1 text-[10px] font-semibold tracking-[0.08em] text-slate-100 hover:border-slate-300"
                      type="button"
                    >
                      {copiedByID[row.id] ? 'COPIED' : 'COPY URL'}
                    </button>
                  </div>
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

                <div className="mt-2 flex items-center gap-1">
                  <div className="flex flex-wrap gap-1">
                    {primaryControls.map((control) => (
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
                  {secondaryControls.length > 0 ? (
                    <div className="ml-auto flex flex-wrap justify-end gap-1">
                      {secondaryControls.map((control) => (
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
                  ) : null}
                </div>
              </li>
            );
          })}
        </ul>
      )}
    </section>
  );
}

function controlsFor(row: DownloadRow): RowControl[] {
  const neutral = 'border border-slate-600 bg-slate-700/35 text-slate-100 hover:border-slate-300';
  const warn = 'border border-amber-400/45 bg-amber-500/15 text-amber-100 hover:bg-amber-500/25';
  const danger = 'border border-rose-400/45 bg-rose-500/15 text-rose-100 hover:bg-rose-500/25';
  const ok = 'border border-emerald-400/45 bg-emerald-500/15 text-emerald-100 hover:bg-emerald-500/25';

  switch (row.status) {
    case 'pending':
      return [
        { action: 'pause', label: 'PAUSE', className: warn, slot: 'primary' },
        { action: 'cancel', label: 'CANCEL', className: danger, slot: 'primary' }
      ];
    case 'downloading':
      return [
        { action: 'pause', label: 'PAUSE', className: warn, slot: 'primary' },
        { action: 'cancel', label: 'CANCEL', className: danger, slot: 'primary' }
      ];
    case 'paused':
      return [
        { action: 'resume', label: 'RESUME', className: ok, slot: 'primary' },
        { action: 'cancel', label: 'CANCEL', className: danger, slot: 'primary' }
      ];
    case 'error':
      return [
        { action: 'resume', label: 'RETRY', className: ok, slot: 'primary' },
        { action: 'remove', label: 'REMOVE', className: neutral, slot: 'secondary' }
      ];
    case 'canceled':
      return [
        { action: 'resume', label: 'RESUME', className: ok, slot: 'primary' },
        { action: 'remove', label: 'REMOVE', className: neutral, slot: 'secondary' }
      ];
    case 'completed':
      return [
        { action: 'play', label: 'PLAY', className: ok, slot: 'primary' },
        { action: 'delete', label: 'DELETE', className: danger, slot: 'secondary' },
        { action: 'remove', label: 'REMOVE', className: neutral, slot: 'secondary' }
      ];
  }
}
