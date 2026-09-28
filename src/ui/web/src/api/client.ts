// Typed client for every endpoint of the core's local API.
//
// In the application window the HttpOnly session cookie (set by /auth)
// authenticates same-origin requests, so plain relative fetches are enough.
// For development against a separately running core, a `?token=` query
// parameter is remembered and sent as a bearer token.

import type {
  APKEntry,
  Body,
  BodyKind,
  ErrorInfo,
  EventDetail,
  EventPage,
  Info,
  QuickFilter,
  Session,
  Snapshot,
  SortKey,
  Status,
  Summary,
  WSFrame,
} from './types';

/** ApiError is every failure of an API call, always with a human title. */
export class ApiError extends Error {
  readonly status: number;
  readonly code: string;
  readonly title: string;
  readonly causes: string[];
  readonly details: string;

  constructor(status: number, info: ErrorInfo) {
    super(info.title);
    this.name = 'ApiError';
    this.status = status;
    this.code = info.code || (status ? `http_${status}` : 'network');
    this.title = info.title;
    this.causes = info.causes ?? [];
    this.details = info.details ?? '';
  }

  toInfo(): ErrorInfo {
    return { title: this.title, causes: this.causes, details: this.details, code: this.code };
  }
}

const STATUS_TITLES: Record<number, string> = {
  400: 'The request was rejected by the core.',
  401: 'The inspector session has expired. Reopen the application window.',
  403: 'The request was refused by the core.',
  404: 'The requested item no longer exists.',
  409: 'The operation is not possible right now.',
  413: 'The upload is too large.',
  500: 'The core reported an internal error.',
  502: 'The core is not responding.',
  503: 'The core is temporarily unavailable.',
};

/** parseErrorBody turns an error response body into an ApiError. It never produces "Unknown error". */
export function parseErrorBody(status: number, statusText: string, body: string): ApiError {
  const trimmed = body.trim();
  if (trimmed) {
    try {
      const parsed: unknown = JSON.parse(trimmed);
      if (parsed && typeof parsed === 'object' && 'error' in parsed) {
        const e = (parsed as { error: unknown }).error;
        if (e && typeof e === 'object' && typeof (e as ErrorInfo).title === 'string' && (e as ErrorInfo).title) {
          const info = e as ErrorInfo;
          return new ApiError(status, {
            title: info.title,
            causes: Array.isArray(info.causes) ? info.causes.filter((c) => typeof c === 'string') : [],
            details: typeof info.details === 'string' ? info.details : '',
            code: typeof info.code === 'string' ? info.code : '',
          });
        }
        if (typeof e === 'string' && e) {
          return new ApiError(status, { title: e });
        }
      }
    } catch {
      // not JSON: fall through to the plain-text handling
    }
  }
  const title = STATUS_TITLES[status] ?? `The request failed (HTTP ${status}${statusText ? ' ' + statusText : ''}).`;
  return new ApiError(status, { title, details: trimmed.slice(0, 4000) });
}

export function networkError(err: unknown): ApiError {
  return new ApiError(0, {
    title: 'Cannot reach the droidpector core.',
    causes: ['The core process may be restarting after a problem; it reconnects automatically.'],
    details: err instanceof Error ? err.message : String(err),
    code: 'network',
  });
}

/** toApiError normalises anything thrown into an ApiError. */
export function toApiError(err: unknown): ApiError {
  if (err instanceof ApiError) return err;
  if (err instanceof TypeError) return networkError(err);
  return new ApiError(0, {
    title: err instanceof Error && err.message ? err.message : 'The operation failed.',
    details: err instanceof Error ? (err.stack ?? '') : String(err),
    code: 'client',
  });
}

// ---- auth (development only) --------------------------------------------------

const TOKEN_KEY = 'apkinspector.token';

function readToken(): string {
  try {
    const q = new URLSearchParams(window.location.search).get('token');
    if (q) {
      sessionStorage.setItem(TOKEN_KEY, q);
      return q;
    }
    return sessionStorage.getItem(TOKEN_KEY) ?? '';
  } catch {
    return '';
  }
}

let token: string | null = null;
function authHeaders(): Record<string, string> {
  if (token === null) token = typeof window === 'undefined' ? '' : readToken();
  return token ? { Authorization: `Bearer ${token}` } : {};
}

// ---- transport ------------------------------------------------------------------

export interface RequestOptions {
  method?: string;
  json?: unknown;
  body?: BodyInit;
  headers?: Record<string, string>;
  signal?: AbortSignal;
}

export async function request(path: string, opts: RequestOptions = {}): Promise<Response> {
  const headers: Record<string, string> = { ...authHeaders(), ...opts.headers };
  let body = opts.body;
  if (opts.json !== undefined) {
    headers['Content-Type'] = 'application/json';
    body = JSON.stringify(opts.json);
  }
  let res: Response;
  try {
    res = await fetch(path, { method: opts.method ?? 'GET', headers, body, signal: opts.signal, credentials: 'same-origin' });
  } catch (err) {
    if (err instanceof DOMException && err.name === 'AbortError') throw err;
    throw networkError(err);
  }
  if (!res.ok) {
    let text = '';
    try {
      text = await res.text();
    } catch {
      // ignore
    }
    throw parseErrorBody(res.status, res.statusText, text);
  }
  return res;
}

async function json<T>(path: string, opts?: RequestOptions): Promise<T> {
  const res = await request(path, opts);
  if (res.status === 204) return undefined as T;
  const text = await res.text();
  if (!text) return undefined as T;
  try {
    return JSON.parse(text) as T;
  } catch (err) {
    throw new ApiError(res.status, { title: 'The core sent an unreadable response.', details: String(err), code: 'bad_response' });
  }
}

async function send(path: string, opts: RequestOptions = {}): Promise<void> {
  await request(path, { method: 'POST', ...opts });
}

const enc = encodeURIComponent;

// ---- endpoints --------------------------------------------------------------------

export interface EventQuery {
  filter: string;
  quick: QuickFilter;
  offset: number;
  limit: number;
  sort: SortKey;
  desc: boolean;
}

export function eventsQueryString(q: EventQuery): string {
  const p = new URLSearchParams();
  if (q.filter) p.set('filter', q.filter);
  if (q.quick && q.quick !== 'all') p.set('quick', q.quick);
  p.set('offset', String(q.offset));
  p.set('limit', String(q.limit));
  if (q.sort && q.sort !== 'seq') p.set('sort', q.sort);
  if (q.desc) p.set('desc', '1');
  return p.toString();
}

export interface UploadProgress {
  loaded: number;
  total: number;
}

export const api = {
  info: () => json<Info>('/api/info'),
  status: () => json<Status>('/api/status'),

  startSandbox: (runtime?: string) => send('/api/sandbox/start', { json: runtime ? { runtime } : {} }),
  stopSandbox: () => send('/api/sandbox/stop'),
  restartSandbox: () => send('/api/sandbox/restart'),
  resetSandbox: () => send('/api/sandbox/reset'),

  snapshots: () => json<Snapshot[]>('/api/snapshots'),
  saveSnapshot: (name: string) => send('/api/snapshots', { json: { name } }),
  restoreSnapshot: (name: string) => send(`/api/snapshots/${enc(name)}/restore`),
  deleteSnapshot: (name: string) => send(`/api/snapshots/${enc(name)}`, { method: 'DELETE' }),

  apks: () => json<APKEntry[]>('/api/apks'),
  runApk: (id: string) => send(`/api/apks/${enc(id)}/run`),
  installApk: (id: string) => send(`/api/apks/${enc(id)}/install`),
  reinstallApk: (id: string) => send(`/api/apks/${enc(id)}/reinstall`),
  appAction: (pkg: string, action: 'launch' | 'stop' | 'clear' | 'uninstall') => send(`/api/apps/${enc(pkg)}/${action}`),
  paste: (text: string) => send('/api/display/paste', { json: { text } }),

  sessions: () => json<Session[]>('/api/sessions'),
  session: (id: string) => json<Session>(`/api/sessions/${enc(id)}`),
  saveSession: (id: string, name: string) => json<Session>(`/api/sessions/${enc(id)}/save`, { method: 'POST', json: { name } }),
  clearSession: (id: string) => send(`/api/sessions/${enc(id)}/clear`),
  deleteSession: (id: string) => send(`/api/sessions/${enc(id)}`, { method: 'DELETE' }),
  startCapture: () => json<Session>('/api/capture/start', { method: 'POST' }),
  stopCapture: () => send('/api/capture/stop'),

  events: (sessionId: string, q: EventQuery, signal?: AbortSignal) =>
    json<EventPage>(`/api/sessions/${enc(sessionId)}/events?${eventsQueryString(q)}`, { signal }),
  event: (id: string, signal?: AbortSignal) => json<EventDetail>(`/api/events/${enc(id)}`, { signal }),
  frames: (id: string, from = 0) => json<WSFrame[]>(`/api/events/${enc(id)}/frames?from=${from}`),
  code: async (id: string, gen: string) => (await request(`/api/events/${enc(id)}/code/${enc(gen)}`)).text(),
  replay: (id: string) => json<Summary>(`/api/events/${enc(id)}/replay`, { method: 'POST' }),

  async body(id: string, part: 'request' | 'response', signal?: AbortSignal): Promise<Body> {
    const res = await request(`/api/events/${enc(id)}/body/${part}?decode=1`, { signal });
    const bytes = res.status === 204 ? new Uint8Array() : new Uint8Array(await res.arrayBuffer());
    const kind = (res.headers.get('X-Body-Kind') as BodyKind | null) ?? (bytes.length ? 'binary' : 'empty');
    return {
      status: res.status,
      bytes,
      kind: bytes.length ? kind : 'empty',
      truncated: res.headers.get('X-Body-Truncated') === 'true',
      size: Number(res.headers.get('X-Body-Size') ?? bytes.length) || bytes.length,
      contentType: res.headers.get('Content-Type') ?? '',
      decodeWarning: res.headers.get('X-Decode-Warning') ?? '',
    };
  },

  diagnostics: () => json<Record<string, unknown>>('/api/diagnostics'),

  /** uploadApk streams the file as the raw request body, reporting progress. */
  uploadApk(file: File, onProgress?: (p: UploadProgress) => void): Promise<APKEntry> {
    return new Promise((resolve, reject) => {
      const xhr = new XMLHttpRequest();
      xhr.open('POST', '/api/apks');
      xhr.setRequestHeader('X-File-Name', enc(file.name));
      xhr.setRequestHeader('Content-Type', 'application/vnd.android.package-archive');
      for (const [k, v] of Object.entries(authHeaders())) xhr.setRequestHeader(k, v);
      xhr.upload.onprogress = (e) => onProgress?.({ loaded: e.loaded, total: e.lengthComputable ? e.total : file.size });
      xhr.onerror = () => reject(networkError(new Error('upload failed')));
      xhr.onload = () => {
        if (xhr.status >= 200 && xhr.status < 300) {
          try {
            resolve(JSON.parse(xhr.responseText) as APKEntry);
          } catch (err) {
            reject(new ApiError(xhr.status, { title: 'The core sent an unreadable response.', details: String(err) }));
          }
        } else {
          reject(parseErrorBody(xhr.status, xhr.statusText, xhr.responseText));
        }
      };
      xhr.send(file);
    });
  },
};

// ---- download links (plain <a href download>) --------------------------------------

export const links = {
  sessionHar: (id: string) => `/api/sessions/${enc(id)}/har`,
  eventHar: (id: string) => `/api/events/${enc(id)}/har`,
  bodyDownload: (id: string, part: 'request' | 'response') => `/api/events/${enc(id)}/body/${part}?decode=1&download=1`,
  diagnosticsBundle: () => '/api/diagnostics/bundle',
};

export function wsUrl(path: string): string {
  const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
  return `${proto}//${window.location.host}${path}`;
}
