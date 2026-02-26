import { useEffect, useState } from 'react';
import { normalizeBaseUrl } from '@/lib/api';
import { loadSettings, saveSettings } from '@/lib/storage';
import { DEFAULT_SETTINGS } from '@/types';

export function OptionsApp() {
  const [serverBaseUrl, setServerBaseUrl] = useState(DEFAULT_SETTINGS.serverBaseUrl);
  const [notificationsEnabled, setNotificationsEnabled] = useState(DEFAULT_SETTINGS.notificationsEnabled);
  const [status, setStatus] = useState('');

  useEffect(() => {
    loadSettings()
      .then((settings) => {
        setServerBaseUrl(settings.serverBaseUrl);
        setNotificationsEnabled(settings.notificationsEnabled);
      })
      .catch((error) => {
        setStatus(`Failed to load settings: ${String(error)}`);
      });
  }, []);

  const onSave = async () => {
    const normalized = normalizeBaseUrl(serverBaseUrl.trim());
    await saveSettings({
      serverBaseUrl: normalized,
      notificationsEnabled
    });
    setStatus('Saved.');
  };

  const testConnection = async () => {
    const normalized = normalizeBaseUrl(serverBaseUrl.trim());
    try {
      const response = await fetch(`${normalized}/healthz`);
      if (!response.ok) {
        throw new Error(`HTTP ${response.status}`);
      }
      const body = await response.text();
      setStatus(`Health check passed: ${body.trim()}`);
    } catch (error) {
      setStatus(`Health check failed: ${String(error)}`);
    }
  };

  return (
    <main className="mx-auto max-w-2xl p-6 text-slate-100">
      <section className="vf-card rounded-md p-5">
        <h1 className="mb-4 text-[14px] font-bold tracking-[0.16em] text-emerald-200">VIDEOFETCH EXTENSION SETTINGS</h1>

        <label className="mb-2 block text-[11px] uppercase tracking-[0.12em] text-slate-300">VideoFetch Base URL</label>
        <input
          className="mb-4 w-full rounded border border-slate-600 bg-black/45 px-3 py-2 outline-none focus:border-emerald-400"
          value={serverBaseUrl}
          onChange={(event) => setServerBaseUrl(event.target.value)}
          placeholder="http://127.0.0.1:8080"
        />

        <label className="mb-5 flex items-center gap-2 text-[12px] text-slate-200">
          <input
            type="checkbox"
            checked={notificationsEnabled}
            onChange={(event) => setNotificationsEnabled(event.target.checked)}
          />
          Enable completion/failure notifications while sidepanel is closed
        </label>

        <div className="flex gap-2">
          <button
            className="rounded border border-emerald-400/45 bg-emerald-500/20 px-3 py-2 text-[11px] font-semibold tracking-[0.1em] text-emerald-100 hover:bg-emerald-500/30"
            onClick={() => void onSave()}
            type="button"
          >
            SAVE
          </button>
          <button
            className="rounded border border-slate-500/50 bg-slate-500/20 px-3 py-2 text-[11px] font-semibold tracking-[0.1em] text-slate-100 hover:bg-slate-500/30"
            onClick={() => void testConnection()}
            type="button"
          >
            TEST CONNECTION
          </button>
        </div>

        {status ? (
          <div className="mt-4 rounded border border-slate-700 bg-black/45 px-3 py-2 text-[11px] text-slate-200">{status}</div>
        ) : null}
      </section>
    </main>
  );
}
