import { afterEach, describe, expect, it, vi } from 'vitest';
import { api, ApiError, eventsQueryString, parseErrorBody, toApiError } from './client';

afterEach(() => vi.unstubAllGlobals());

describe('parseErrorBody', () => {
  it('parses the core error envelope', () => {
    const e = parseErrorBody(409, 'Conflict', JSON.stringify({ error: { title: 'Hardware acceleration is unavailable.', causes: ['WHPX disabled'], details: 'qemu: ...', code: 'no_accel' } }));
    expect(e).toBeInstanceOf(ApiError);
    expect(e.status).toBe(409);
    expect(e.title).toBe('Hardware acceleration is unavailable.');
    expect(e.causes).toEqual(['WHPX disabled']);
    expect(e.details).toBe('qemu: ...');
    expect(e.code).toBe('no_accel');
  });

  it('parses bad_filter errors', () => {
    const e = parseErrorBody(400, '', '{"error":{"title":"filter error at position 1: value expected for \\"method\\"","code":"bad_filter"}}\n');
    expect(e.code).toBe('bad_filter');
    expect(e.title).toContain('value expected');
    expect(e.causes).toEqual([]);
  });

  it('never produces "Unknown error" for non-JSON bodies', () => {
    const e = parseErrorBody(502, 'Bad Gateway', '<html>proxy</html>');
    expect(e.title).toBe('The core is not responding.');
    expect(e.details).toBe('<html>proxy</html>');
    expect(e.code).toBe('http_502');
    const e2 = parseErrorBody(418, "I'm a teapot", '');
    expect(e2.title).toBe("The request failed (HTTP 418 I'm a teapot).");
    expect(e2.title.toLowerCase()).not.toContain('unknown');
  });

  it('handles malformed envelopes', () => {
    expect(parseErrorBody(500, '', '{"error":{}}').title).toBe('The core reported an internal error.');
    expect(parseErrorBody(500, '', '{"error":"boom"}').title).toBe('boom');
    expect(parseErrorBody(404, '', 'not found').title).toBe('The requested item no longer exists.');
  });
});

describe('toApiError', () => {
  it('maps network failures and plain errors', () => {
    expect(toApiError(new TypeError('Failed to fetch')).code).toBe('network');
    expect(toApiError(new Error('x')).title).toBe('x');
    const e = new ApiError(400, { title: 't' });
    expect(toApiError(e)).toBe(e);
  });
});

describe('request', () => {
  it('throws ApiError from error responses', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => new Response(JSON.stringify({ error: { title: 'Start the sandbox to capture traffic.', code: 'not_running' } }), { status: 409 })),
    );
    await expect(api.startCapture()).rejects.toMatchObject({ code: 'not_running', title: 'Start the sandbox to capture traffic.', status: 409 });
  });

  it('throws a network ApiError when the core is unreachable', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => Promise.reject(new TypeError('Failed to fetch'))));
    await expect(api.status()).rejects.toMatchObject({ code: 'network', title: 'Cannot reach the droidpector core.' });
  });

  it('reads body metadata headers', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => new Response('{"a":1}', { status: 200, headers: { 'X-Body-Kind': 'json', 'X-Body-Truncated': 'true', 'X-Body-Size': '999', 'Content-Type': 'application/json' } })),
    );
    const b = await api.body('e1', 'response');
    expect(b.kind).toBe('json');
    expect(b.truncated).toBe(true);
    expect(b.size).toBe(999);
    expect(new TextDecoder().decode(b.bytes)).toBe('{"a":1}');
  });

  it('treats 204 bodies as empty', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response(null, { status: 204 })));
    const b = await api.body('e1', 'request');
    expect(b.kind).toBe('empty');
    expect(b.bytes.length).toBe(0);
  });
});

describe('eventsQueryString', () => {
  it('omits defaults', () => {
    expect(eventsQueryString({ filter: '', quick: 'all', offset: 0, limit: 100, sort: 'seq', desc: false })).toBe('offset=0&limit=100');
    expect(eventsQueryString({ filter: 'method:POST', quick: 'image', offset: 200, limit: 100, sort: 'size', desc: true })).toBe(
      'filter=method%3APOST&quick=image&offset=200&limit=100&sort=size&desc=1',
    );
  });
});
