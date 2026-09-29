import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';
import type { Body, WSFrame } from '../../api/types';
import { dnsEvent, httpGet, pinnedTls, replayed, wsEvent, wsFrames } from '../../test/fixtures';
import { BodyViewer, defaultMode, LARGE_BODY } from './body/BodyViewer';
import { DetailsView } from './DetailsPanel';
import { ENCRYPTED_MESSAGE, reasonText } from './EncryptedNotice';
import { headersText, sortHeaders } from './HeadersTab';
import { waterfall } from './InfoTabs';
import { MessagesTab, framePreview } from './MessagesTab';
import { tabsFor } from './tabsFor';

function tabNames() {
  return screen.getAllByRole('tab').map((t) => t.textContent);
}

describe('tabsFor', () => {
  it('shows HTTP tabs for HTTP events', () => {
    expect(tabsFor(httpGet)).toEqual(['overview', 'headers', 'query', 'request', 'response', 'timing', 'connection']);
  });
  it('shows a DNS tab instead of HTTP tabs', () => {
    expect(tabsFor(dnsEvent)).toEqual(['overview', 'dns', 'connection']);
  });
  it('shows Messages for WebSockets', () => {
    expect(tabsFor(wsEvent)).toEqual(['overview', 'headers', 'messages', 'connection']);
  });
  it('shows only metadata for encrypted connections', () => {
    expect(tabsFor(pinnedTls)).toEqual(['overview', 'connection']);
  });
});

describe('DetailsView', () => {
  it('renders the overview of an HTTP request', () => {
    render(<DetailsView d={httpGet} tab="overview" onTab={() => {}} />);
    expect(tabNames()).toEqual(['Overview', 'Headers', 'Query', 'Request', 'Response', 'Timing', 'Connection']);
    const panel = screen.getByRole('tabpanel');
    expect(within(panel).getByText('https://api.example.test/test/get?page=1&q=hello%20world')).toBeTruthy();
    expect(within(panel).getByText('200 OK')).toBeTruthy();
    expect(within(panel).getByText('HTTP/2')).toBeTruthy();
    expect(within(panel).getByText('127.0.0.1:51330')).toBeTruthy();
    expect(screen.getByRole('button', { name: /Replay/ }).hasAttribute('disabled')).toBe(false);
    expect(screen.getByRole('tab', { name: 'Overview' }).getAttribute('aria-selected')).toBe('true');
  });

  it('links a replay to its original request', () => {
    render(<DetailsView d={replayed} tab="overview" onTab={() => {}} />);
    expect(screen.getByRole('button', { name: 'original request' })).toBeTruthy();
    expect(screen.getAllByText('replay').length).toBeGreaterThan(0);
  });

  it('switches tabs with the keyboard', () => {
    const onTab = vi.fn();
    render(<DetailsView d={httpGet} tab="overview" onTab={onTab} />);
    fireEvent.keyDown(screen.getByRole('tab', { name: 'Overview' }), { key: 'ArrowRight' });
    expect(onTab).toHaveBeenCalledWith('headers');
  });

  it('renders sorted headers with a raw toggle', () => {
    render(<DetailsView d={httpGet} tab="headers" onTab={() => {}} />);
    const req = screen.getByRole('region', { name: 'Request headers' });
    const names = within(req)
      .getAllByRole('rowheader')
      .map((th) => th.textContent);
    expect(names).toEqual(['Accept-Encoding', 'Authorization', 'User-Agent']);
    fireEvent.click(screen.getByRole('checkbox', { name: 'Raw' }));
    expect(within(req).getByText(/GET \/test\/get\?page=1&q=hello%20world HTTP\/2/)).toBeTruthy();
  });

  it('renders the query parameters table', () => {
    render(<DetailsView d={httpGet} tab="query" onTab={() => {}} />);
    expect(screen.getByText('hello world')).toBeTruthy();
    expect(screen.getByText('page')).toBeTruthy();
  });

  it('renders timing with n/a phases', () => {
    render(<DetailsView d={httpGet} tab="timing" onTab={() => {}} />);
    expect(screen.getAllByText('n/a')).toHaveLength(3);
    expect(screen.getByText('Waiting for server response')).toBeTruthy();
    expect(screen.getByText('0.19 ms')).toBeTruthy();
  });

  it('renders connection and certificate details', () => {
    render(<DetailsView d={httpGet} tab="connection" onTab={() => {}} />);
    expect(screen.getByText('TLS_AES_128_GCM_SHA256')).toBeTruthy();
    expect(screen.getByText('CN=droidpector Test Server CA')).toBeTruthy();
    expect(screen.getByText('Yes — decrypted by the sandbox')).toBeTruthy();
    expect(screen.getByText('10.0.2.15:18749')).toBeTruthy();
    // Which host interface (e.g. the VPN adapter) the connection used.
    expect(screen.getByText('OpenVPN TAP-Windows6')).toBeTruthy();
    expect(screen.getByText(/10\.8\.0\.6:51330/)).toBeTruthy();
  });

  it('renders DNS details', () => {
    render(<DetailsView d={dnsEvent} tab="dns" onTab={() => {}} />);
    expect(tabNames()).toEqual(['Overview', 'DNS', 'Connection']);
    expect(screen.getByText('NOERROR')).toBeTruthy();
    expect(screen.getByText('198.18.0.1')).toBeTruthy();
    expect(screen.getByText('60s')).toBeTruthy();
  });

  it('shows the encrypted-traffic message prominently', () => {
    render(<DetailsView d={pinnedTls} tab="overview" onTab={() => {}} />);
    expect(screen.getByText(ENCRYPTED_MESSAGE)).toBeTruthy();
    expect(screen.getByText(/the app rejected the sandbox certificate/i)).toBeTruthy();
    expect(screen.queryByRole('button', { name: /Replay/ })).toBeNull();
  });
});

describe('reasonText', () => {
  it('strips the generic prefix', () => {
    expect(reasonText(pinnedTls.tls?.passthroughReason)).toMatch(/^The app rejected the sandbox certificate/);
    expect(reasonText('MITM disabled')).toBe('MITM disabled');
    expect(reasonText(undefined)).toBe('');
  });
});

describe('header helpers', () => {
  it('sorts case-insensitively and renders raw text', () => {
    expect(sortHeaders([{ name: 'b', value: '1' }, { name: 'A', value: '2' }]).map((h) => h.name)).toEqual(['A', 'b']);
    expect(headersText([{ name: 'X', value: 'y' }], 'GET / HTTP/1.1')).toBe('GET / HTTP/1.1\nX: y');
  });
});

describe('waterfall', () => {
  it('places phases sequentially with TLS inside connect', () => {
    const w = waterfall({ blocked: 1, dns: 2, connect: 10, tls: 6, send: 1, wait: 5, receive: 3 });
    const at = (k: string) => w.bars.find((b) => b.key === k);
    expect(at('dns')).toMatchObject({ start: 1, duration: 2 });
    expect(at('connect')).toMatchObject({ start: 3, duration: 10 });
    expect(at('tls')).toMatchObject({ start: 7, duration: 6 });
    expect(at('send')).toMatchObject({ start: 13 });
    expect(w.total).toBe(22);
  });
});

function body(text: string | Uint8Array, kind: Body['kind'], extra: Partial<Body> = {}): Body {
  const bytes = typeof text === 'string' ? new TextEncoder().encode(text) : text;
  return { status: 200, bytes, kind, truncated: false, size: bytes.length, contentType: '', decodeWarning: '', ...extra };
}

describe('BodyViewer', () => {
  it('shows a JSON tree with collapse/expand and search', async () => {
    const load = vi.fn(async () => body('{"id":42,"tags":["android","network"],"nested":{"ok":true,"deep":{"deeper":{"needle":"x"}}}}', 'json'));
    render(<BodyViewer eventId="e1" part="response" bodyRef={httpGet.responseBody} load={load} />);
    const tree = await screen.findByRole('tree');
    expect(within(tree).getByText('"android"')).toBeTruthy();
    // depth-2 default: /nested/deep/deeper is collapsed
    expect(within(tree).queryByText('"x"')).toBeNull();
    fireEvent.change(screen.getByRole('searchbox', { name: 'Search JSON' }), { target: { value: 'needle' } });
    expect(within(tree).getByText('needle', { selector: 'mark' })).toBeTruthy();
    expect(screen.getByText(/1 match/)).toBeTruthy();
    fireEvent.change(screen.getByRole('searchbox', { name: 'Search JSON' }), { target: { value: '' } });
    fireEvent.click(screen.getByRole('button', { name: 'Collapse all' }));
    expect(within(tree).queryByText('"android"')).toBeNull();
    fireEvent.click(screen.getByRole('button', { name: 'Expand all' }));
    expect(within(tree).getByText('"x"')).toBeTruthy();
    fireEvent.click(screen.getByRole('button', { name: 'Raw' }));
    expect(screen.getByText(/"needle": "x"/)).toBeTruthy();
  });

  it('shows truncation and decode warnings', async () => {
    const load = vi.fn(async () => body('hello', 'text', { truncated: true, decodeWarning: 'could not decode br' }));
    render(<BodyViewer eventId="e1" part="response" bodyRef={{ hash: 'h', size: 10_000, stored: 5, truncated: true }} load={load} />);
    expect(await screen.findByText(/Body truncated: 5 B of 10,000 B captured/)).toBeTruthy();
    expect(screen.getByText('could not decode br')).toBeTruthy();
    expect(screen.getByText('hello')).toBeTruthy();
  });

  it('shows "No body" for empty bodies', async () => {
    render(<BodyViewer eventId="e1" part="request" bodyRef={undefined} />);
    expect(screen.getByText('No body')).toBeTruthy();
    const load = vi.fn(async () => body('', 'empty', { status: 204 }));
    render(<BodyViewer eventId="e2" part="request" bodyRef={{ hash: 'h', size: 0, stored: 0 }} load={load} />);
    await waitFor(() => expect(screen.getAllByText('No body')).toHaveLength(2));
  });

  it('shows binary bodies as a hex dump with a download link', async () => {
    const load = vi.fn(async () => body(new Uint8Array([0, 1, 2, 0x41, 0xff]), 'binary'));
    render(<BodyViewer eventId="e1" part="response" bodyRef={{ hash: 'h', size: 5, stored: 5 }} load={load} />);
    expect(await screen.findByLabelText('Hex dump')).toBeTruthy();
    expect(screen.getByLabelText('Hex dump').textContent).toContain('00 01 02 41 ff');
    expect(screen.getByRole('link', { name: /Download/ }).getAttribute('href')).toBe('/api/events/e1/body/response?decode=1&download=1');
  });

  it('previews images through a blob URL', async () => {
    const create = vi.fn(() => 'blob:preview');
    const revoke = vi.fn();
    Object.assign(URL, { createObjectURL: create, revokeObjectURL: revoke });
    const load = vi.fn(async () => body(new Uint8Array([0x89, 0x50, 0x4e, 0x47]), 'image', { contentType: 'image/png' }));
    render(<BodyViewer eventId="e1" part="response" bodyRef={{ hash: 'h', size: 4, stored: 4, mime: 'image/png' }} load={load} />);
    const img = await screen.findByRole('img', { name: 'Response image preview' });
    expect(img.getAttribute('src')).toBe('blob:preview');
  });

  it('renders HTML as highlighted source, never as a page', async () => {
    const load = vi.fn(async () => body('<html><body><script>alert(1)</script><p>Hi</p></body></html>', 'html'));
    const { container } = render(<BodyViewer eventId="e1" part="response" bodyRef={{ hash: 'h', size: 60, stored: 60 }} load={load} />);
    await screen.findByText('Pretty');
    expect(container.querySelector('script')).toBeNull();
    expect(container.querySelector('iframe')).toBeNull();
    expect(container.textContent).toContain('alert(1)');
    expect(container.querySelector('.m-tag')).toBeTruthy();
  });

  it('shows raw TCP stream prefixes as hex with a note', async () => {
    const load = vi.fn(async () => body(new Uint8Array([0xef, 0xee, 0xee, 0xee, 0x01]), 'binary'));
    render(<BodyViewer eventId="e1" part="request" bodyRef={{ hash: 'h', size: 4096, stored: 5 }} rawStream load={load} />);
    expect(await screen.findByLabelText('Hex dump')).toBeTruthy();
    expect(screen.getByRole('note').textContent).toMatch(/Raw TCP stream \(sent by the app, first 5 B of 4,096 B\)/);
    expect(screen.getByText('STREAM · 5 B')).toBeTruthy();
  });

  it('opens large JSON bodies in raw mode', () => {
    expect(defaultMode('json', LARGE_BODY + 1)).toBe('raw');
    expect(defaultMode('json', 100)).toBe('tree');
    expect(defaultMode('binary', 10)).toBe('hex');
  });
});

describe('MessagesTab', () => {
  it('lists frames with direction and shows full data', async () => {
    const loadFrames = vi.fn(async () => wsFrames as WSFrame[]);
    render(<MessagesTab d={wsEvent} loadFrames={loadFrames} />);
    const list = await screen.findByRole('listbox', { name: 'WebSocket messages' });
    const options = within(list).getAllByRole('option');
    expect(options).toHaveLength(4);
    expect(within(options[0]).getByLabelText('Received')).toBeTruthy();
    expect(within(options[1]).getByLabelText('Sent')).toBeTruthy();
    expect(within(options[3]).getByText('close 1000')).toBeTruthy();
    await act(async () => fireEvent.click(options[2]));
    expect(screen.getByRole('button', { name: 'JSON' }).getAttribute('aria-pressed')).toBe('true');
    expect(screen.getByRole('tree')).toBeTruthy();
    fireEvent.click(screen.getByRole('button', { name: 'Hex' }));
    expect(screen.getByLabelText('Hex dump').textContent).toContain('7b 22 74 79');
  });

  it('previews frames', () => {
    expect(framePreview(wsFrames[1] as WSFrame)).toBe('hello');
    expect(framePreview({ seq: 1, time: '', outgoing: true, opcode: 2, length: 3000 })).toBe('binary, 3.0 kB');
  });
});
