import { describe, expect, it } from 'vitest';
import type { EventDetail, Summary } from '../../api/types';
import { httpGet, pinnedTls } from '../../test/fixtures';
import { isRawStream, tabsFor } from '../details/tabsFor';
import { methodLabel, methodTone, statusText, statusTone, typeLabel } from './columns';

const base: Summary = {
  id: 'x',
  seq: 1,
  kind: 'tcp',
  category: 'other',
  state: 'pending',
  initiator: 'guest',
  timestamp: '2026-09-28T13:34:36Z',
  durationMs: 1200,
  host: '149.154.167.51',
  port: 443,
  requestSize: 512,
  responseSize: 0,
};

describe('network columns', () => {
  it('shows the protocol label in the Type column for raw flows', () => {
    expect(typeLabel({ ...base, protocol: 'MTProto' })).toBe('MTProto');
    expect(typeLabel({ ...base, kind: 'tls', protocol: 'TLS' })).toBe('TLS');
    expect(typeLabel({ ...base, kind: 'udp', protocol: 'UDP' })).toBe('UDP');
    // degrades gracefully when the core does not send a protocol
    expect(typeLabel({ ...base, protocol: undefined })).toBe('TCP');
    expect(typeLabel({ ...base, kind: 'dns', protocol: undefined })).toBe('DNS');
  });

  it('uses the MIME subtype for HTTP rows', () => {
    expect(typeLabel({ ...base, kind: 'http', category: 'api', mime: 'application/json; charset=utf-8' })).toBe('json');
    expect(typeLabel({ ...base, kind: 'http', category: 'image', mime: 'image/svg+xml' })).toBe('svg');
    expect(typeLabel({ ...base, kind: 'http', category: 'document' })).toBe('document');
    expect(typeLabel({ ...base, kind: 'websocket' })).toBe('websocket');
  });

  it('labels and colors the Method column', () => {
    expect(methodLabel({ ...base, kind: 'http', method: 'POST' })).toBe('POST');
    expect(methodTone({ ...base, kind: 'http', method: 'POST' })).toBe('post');
    expect(methodTone({ ...base, kind: 'http', method: 'GET' })).toBe('get');
    expect(methodTone({ ...base, kind: 'http', method: 'PATCH' })).toBe('put');
    expect(methodTone({ ...base, kind: 'http', method: 'DELETE' })).toBe('delete');
    expect(methodLabel(base)).toBe('TCP');
    expect(methodTone(base)).toBe('neutral');
    expect(methodLabel({ ...base, kind: 'dns', method: 'AAAA' })).toBe('AAAA');
    expect(methodLabel({ ...base, kind: 'websocket' })).toBe('WS');
  });

  it('classifies status values', () => {
    expect(statusTone({ ...base, kind: 'http', state: 'complete', status: 204 })).toBe('ok');
    expect(statusTone({ ...base, kind: 'http', state: 'complete', status: 302 })).toBe('redirect');
    expect(statusTone({ ...base, kind: 'http', state: 'complete', status: 404 })).toBe('client');
    expect(statusTone({ ...base, kind: 'http', state: 'complete', status: 503 })).toBe('server');
    expect(statusTone({ ...base, kind: 'http', state: 'error', error: 'reset' })).toBe('server');
    expect(statusTone(base)).toBe('pending');
    expect(statusText({ ...base, state: 'complete', encrypted: true })).toBe('—');
    expect(statusText({ ...base, kind: 'dns', state: 'complete' })).toBe('ok');
  });
});

describe('raw stream details', () => {
  const raw: EventDetail = {
    ...pinnedTls,
    id: 'raw1',
    kind: 'tcp',
    protocol: 'MTProto',
    encrypted: false,
    tls: undefined,
    url: 'tcp://149.154.167.51:443',
    requestBody: { hash: 'a', size: 4096, stored: 4096 },
    responseBody: { hash: 'b', size: 1024, stored: 1024 },
  };

  it('adds Request/Response tabs when a raw flow carries stream prefixes', () => {
    expect(isRawStream(raw)).toBe(true);
    expect(isRawStream(httpGet)).toBe(false);
    expect(tabsFor(raw)).toEqual(['overview', 'request', 'response', 'connection']);
    expect(tabsFor({ ...raw, responseBody: undefined })).toEqual(['overview', 'request', 'connection']);
    expect(tabsFor({ ...raw, requestBody: undefined, responseBody: undefined })).toEqual(['overview', 'connection']);
  });
});
