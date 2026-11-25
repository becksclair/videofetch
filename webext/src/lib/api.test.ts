import { describe, it, afterEach } from 'node:test';
import assert from 'node:assert/strict';
import {
  AuthRequiredError,
  checkHealth,
  clearHistory,
  enqueueSingle,
  fetchDownloads,
  isAuthRequiredError,
  wsUrl
} from './api';

const originalFetch = globalThis.fetch;

afterEach(() => {
  globalThis.fetch = originalFetch;
});

function mockFetch(handler: (input: RequestInfo | URL, init?: RequestInit) => Response | Promise<Response>) {
  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => handler(input, init)) as typeof fetch;
}

describe('webext api client (Cloudflare Access support)', () => {
  it('converts https base URL to wss for downloads stream', () => {
    assert.equal(
      wsUrl('https://videofetch.heliasar.com/', 120),
      'wss://videofetch.heliasar.com/api/ws/downloads?limit=120&sort=updated_at&order=desc'
    );
  });

  it('sends credentials=include and redirect=manual without Content-Type on GET', async () => {
    let seenInit: RequestInit | undefined;
    mockFetch((_input, init) => {
      seenInit = init;
      return new Response(JSON.stringify({ status: 'success', downloads: [] }), {
        status: 200,
        headers: { 'content-type': 'application/json' }
      });
    });

    const rows = await fetchDownloads('https://videofetch.heliasar.com', { limit: 10 });
    assert.deepEqual(rows, []);
    assert.equal(seenInit?.credentials, 'include');
    assert.equal(seenInit?.redirect, 'manual');
    const headers = new Headers(seenInit?.headers);
    assert.equal(headers.has('content-type'), false);
  });

  it('sets Content-Type on JSON POST with body and omits it on bodyless DELETE', async () => {
    const calls: RequestInit[] = [];
    mockFetch((_input, init) => {
      calls.push(init ?? {});
      return new Response(JSON.stringify({ status: 'success', count: 2 }), {
        status: 200,
        headers: { 'content-type': 'application/json' }
      });
    });

    await enqueueSingle('https://videofetch.heliasar.com', 'https://example.com/v');
    const count = await clearHistory('https://videofetch.heliasar.com');
    assert.equal(count, 2);
    assert.equal(new Headers(calls[0].headers).get('content-type'), 'application/json');
    assert.equal(new Headers(calls[1].headers).has('content-type'), false);
  });

  it('throws AuthRequiredError on Cloudflare Access 302 or opaqueredirect', async () => {
    mockFetch(() =>
      new Response('<html>302</html>', {
        status: 302,
        headers: {
          location: 'https://broad-credit-db39.cloudflareaccess.com/cdn-cgi/access/login/videofetch.heliasar.com',
          'www-authenticate': 'Cloudflare-Access'
        }
      })
    );

    await assert.rejects(() => fetchDownloads('https://videofetch.heliasar.com'), isAuthRequiredError);
    await assert.rejects(() => checkHealth('https://videofetch.heliasar.com'), isAuthRequiredError);

    mockFetch(() => ({ type: 'opaqueredirect', status: 0, ok: false, headers: new Headers() } as Response));
    await assert.rejects(() => fetchDownloads('https://videofetch.heliasar.com'), AuthRequiredError);
    await assert.rejects(() => checkHealth('https://videofetch.heliasar.com'), AuthRequiredError);
  });

  it('does not misclassify JSON 403 forbidden_origin as AuthRequiredError', async () => {
    mockFetch(() =>
      new Response(JSON.stringify({ status: 'error', message: 'forbidden_origin' }), {
        status: 403,
        headers: { 'content-type': 'application/json' }
      })
    );

    await assert.rejects(
      () => enqueueSingle('https://videofetch.heliasar.com', 'https://example.com/v'),
      (err: unknown) => !isAuthRequiredError(err) && err instanceof Error && err.message === 'forbidden_origin'
    );
  });

  it('validates /healthz returns ok and rejects HTML login pages', async () => {
    mockFetch(() => new Response('ok', { status: 200, headers: { 'content-type': 'text/plain' } }));
    assert.equal(await checkHealth('https://videofetch.heliasar.com'), 'ok');

    mockFetch(() => new Response('<html>login</html>', { status: 200, headers: { 'content-type': 'text/html' } }));
    await assert.rejects(() => checkHealth('https://videofetch.heliasar.com'), AuthRequiredError);
  });

  it('treats HTML 5xx tunnel outages as HTTP errors instead of AuthRequiredError', async () => {
    for (const status of [502, 503, 530]) {
      mockFetch(() => new Response('<html>bad gateway</html>', { status, headers: { 'content-type': 'text/html' } }));
      await assert.rejects(
        () => fetchDownloads('https://videofetch.heliasar.com'),
        (err: unknown) => !isAuthRequiredError(err) && err instanceof Error && err.message === `HTTP ${status}`
      );
      await assert.rejects(
        () => checkHealth('https://videofetch.heliasar.com'),
        (err: unknown) => !isAuthRequiredError(err) && err instanceof Error && err.message === `HTTP ${status}`
      );
    }
  });

  it('treats non-JSON 401/403 responses as AuthRequiredError', async () => {
    for (const status of [401, 403]) {
      mockFetch(() => new Response('<html>unauthorized</html>', { status, headers: { 'content-type': 'text/html' } }));
      await assert.rejects(() => fetchDownloads('https://videofetch.heliasar.com'), AuthRequiredError);
      await assert.rejects(() => checkHealth('https://videofetch.heliasar.com'), AuthRequiredError);
    }
  });
});
