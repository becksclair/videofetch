import { useEffect, useState } from 'react';
import { checkHealth, isAuthRequiredError, normalizeBaseUrl } from '@/lib/api';
import { loadSettings, saveSettings } from '@/lib/storage';
import { ext, originPermissionForBaseUrl } from '@/lib/webext';
import { DEFAULT_SETTINGS } from '@/types';

async function ensureServerPermission(baseUrl: string): Promise<void> {
  const origins = [originPermissionForBaseUrl(baseUrl)];
  const permissions = ext.permissions;
  if (!permissions) {
    return;
  }
  const granted = await permissions.request({ origins });
  if (!granted) {
    throw new Error('server_origin_permission_denied');
  }
}

export function OptionsApp() {
  const [serverBaseUrl, setServerBaseUrl] = useState(DEFAULT_SETTINGS.serverBaseUrl);
  const [notificationsEnabled, setNotificationsEnabled] = useState(DEFAULT_SETTINGS.notificationsEnabled);
  const [status, setStatus] = useState('');
  const [needsAuth, setNeedsAuth] = useState(false);

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

  const openSignIn = () => {
    const normalized = normalizeBaseUrl(serverBaseUrl.trim());
    if (!normalized) {
      return;
    }
    void ext.tabs.create({ url: normalized });
  };

  const onSave = async () => {
    setNeedsAuth(false);
    try {
      const normalized = normalizeBaseUrl(serverBaseUrl.trim());
      await ensureServerPermission(normalized);
      await saveSettings({
        serverBaseUrl: normalized,
        notificationsEnabled
      });
      setStatus('Saved.');
    } catch (error) {
      setStatus(`Save failed: ${String(error)}`);
    }
  };

  const testConnection = async () => {
    const normalized = normalizeBaseUrl(serverBaseUrl.trim());
    setNeedsAuth(false);
    try {
      await ensureServerPermission(normalized);
      const body = await checkHealth(normalized);
      setStatus(`Health check passed: ${body}`);
    } catch (error) {
      if (isAuthRequiredError(error)) {
        setNeedsAuth(true);
        setStatus('Sign-in required (Cloudflare Access). Open the server URL to authenticate, then test again.');
        return;
      }
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
          onChange={(event) => {
            setServerBaseUrl(event.target.value);
            setNeedsAuth(false);
          }}
          placeholder="https://videofetch.heliasar.com"
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
          <div className="mt-4 rounded border border-slate-700 bg-black/45 px-3 py-2 text-[11px] text-slate-200">
            <div>{status}</div>
            {needsAuth ? (
              <button
                className="mt-2 rounded border border-amber-300/45 bg-amber-400/15 px-2.5 py-1 text-[10px] font-semibold tracking-[0.08em] text-amber-100 hover:bg-amber-400/25"
                onClick={openSignIn}
                type="button"
              >
                OPEN SIGN-IN
              </button>
            ) : null}
          </div>
        ) : null}
      </section>
    </main>
  );
}
