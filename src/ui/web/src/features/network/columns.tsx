import type { ReactNode } from 'react';
import type { SortKey, Summary } from '../../api/types';
import { Icon } from '../../components/Icon';
import { formatBytes, formatClock, formatDuration } from '../../lib/format';
import { loadJSON, saveJSON } from '../../lib/storage';

export interface Column {
  id: string;
  label: string;
  sort: SortKey;
  width: number;
  min: number;
  align?: 'right';
  render: (s: Summary) => ReactNode;
  title?: (s: Summary) => string;
}

export function isErrorRow(s: Summary): boolean {
  return s.state === 'error' || !!s.error || (s.status ?? 0) >= 400;
}

export function isHttpKind(kind: Summary['kind']): boolean {
  return kind === 'http' || kind === 'websocket';
}

/** methodLabel is the text of the Method column: the HTTP method, or the flow kind for raw flows. */
export function methodLabel(s: Summary): string {
  if (isHttpKind(s.kind)) return s.method || (s.kind === 'websocket' ? 'WS' : '');
  if (s.kind === 'dns') return s.method || 'DNS';
  return s.kind.toUpperCase();
}

/** methodTone classifies a method for its badge color. */
export function methodTone(s: Summary): 'get' | 'post' | 'put' | 'delete' | 'neutral' {
  if (!isHttpKind(s.kind)) return 'neutral';
  switch ((s.method ?? '').toUpperCase()) {
    case 'GET':
    case 'HEAD':
    case 'OPTIONS':
      return 'get';
    case 'POST':
      return 'post';
    case 'PUT':
    case 'PATCH':
      return 'put';
    case 'DELETE':
      return 'delete';
    default:
      return 'neutral';
  }
}

/** typeLabel is the Type column: the MIME subtype for HTTP, the protocol label (TCP, TLS, UDP, DNS, MTProto…) otherwise. */
export function typeLabel(s: Summary): string {
  switch (s.kind) {
    case 'dns':
      return s.protocol || 'DNS';
    case 'websocket':
      return 'websocket';
    case 'tls':
    case 'tcp':
    case 'udp':
      return s.protocol || s.kind.toUpperCase();
  }
  if (s.mime) {
    const sub = s.mime.split(';')[0].split('/')[1] ?? s.mime;
    return sub.replace(/^x-/, '').replace(/\+.*$/, '') || s.category;
  }
  return s.category;
}

export function statusText(s: Summary): string {
  if (s.state === 'pending') return '(pending)';
  if (s.status) return String(s.status);
  if (s.state === 'error') return '(failed)';
  if (s.kind === 'dns') return s.error ? '(failed)' : 'ok';
  if (s.encrypted) return '—';
  return '';
}

/** statusTone classifies the Status column value for its color. */
export function statusTone(s: Summary): 'ok' | 'redirect' | 'client' | 'server' | 'pending' | 'muted' {
  if (s.state === 'pending') return 'pending';
  if (s.state === 'error' || (s.error && !s.status)) return 'server';
  if (s.status) {
    if (s.status >= 500) return 'server';
    if (s.status >= 400) return 'client';
    if (s.status >= 300) return 'redirect';
    return 'ok';
  }
  if (s.kind === 'dns') return s.error ? 'server' : 'ok';
  return 'muted';
}

/** isLongLived tells whether a row is a raw stream whose sizes grow while pending. */
function hasLiveSize(s: Summary): boolean {
  return s.state === 'pending' && (s.requestSize > 0 || s.responseSize > 0);
}

function Pending({ label }: { label: string }) {
  return (
    <span className="cell-pending" role="img" aria-label={label} title={label}>
      <span className="spinner tiny" aria-hidden="true" />
    </span>
  );
}

export const COLUMNS: Column[] = [
  {
    id: 'method',
    label: 'Method',
    sort: 'method',
    width: 76,
    min: 52,
    render: (s) => <span className={`method-badge tone-${methodTone(s)}`}>{methodLabel(s)}</span>,
    title: (s) => (isHttpKind(s.kind) ? s.method ?? '' : `${s.kind.toUpperCase()} flow`),
  },
  {
    id: 'host',
    label: 'Host',
    sort: 'host',
    width: 190,
    min: 60,
    render: (s) => (
      <>
        {s.initiator === 'replay' && (
          <span className="replay-icon" title="Replayed request" aria-label="Replayed request">
            <Icon name="replay" size={11} />
          </span>
        )}
        <span className="cell-host">{s.host}</span>
      </>
    ),
    title: (s) => s.host ?? '',
  },
  {
    id: 'path',
    label: 'Path',
    sort: 'path',
    width: 280,
    min: 80,
    render: (s) => (
      <>
        {s.encrypted && (
          <span className="badge encrypted" title="HTTPS encrypted traffic: payload inspection unavailable">
            <Icon name="lock" size={10} /> encrypted
          </span>
        )}
        {s.path ? (
          <>
            {s.path}
            {s.query ? <span className="query">?{s.query}</span> : null}
          </>
        ) : s.port && !isHttpKind(s.kind) && s.kind !== 'dns' ? (
          <span className="query">:{s.port}</span>
        ) : null}
      </>
    ),
    title: (s) => (s.path ?? '') + (s.query ? `?${s.query}` : ''),
  },
  {
    id: 'status',
    label: 'Status',
    sort: 'status',
    width: 68,
    min: 48,
    render: (s) => (s.state === 'pending' ? <Pending label="Pending" /> : <span className={`status-text tone-${statusTone(s)}`}>{statusText(s)}</span>),
    title: (s) => s.error || statusText(s),
  },
  {
    id: 'type',
    label: 'Type',
    sort: 'type',
    width: 92,
    min: 48,
    render: (s) => <span className={`type-chip kind-${s.kind}`}>{typeLabel(s)}</span>,
    title: (s) => s.mime || s.protocol || s.category,
  },
  {
    id: 'size',
    label: 'Size',
    sort: 'size',
    width: 76,
    min: 48,
    align: 'right',
    render: (s) => {
      if (s.kind === 'dns') return '';
      if (s.state === 'pending' && !hasLiveSize(s)) return <span className="shimmer" aria-label="Pending" />;
      return <span className="num">{formatBytes(s.responseSize)}</span>;
    },
    title: (s) => `Request ${formatBytes(s.requestSize)} · Response ${formatBytes(s.responseSize)}`,
  },
  {
    id: 'duration',
    label: 'Duration',
    sort: 'duration',
    width: 80,
    min: 48,
    align: 'right',
    render: (s) => {
      if (s.state === 'pending') return s.durationMs > 0 ? <span className="num live">{formatDuration(s.durationMs)}</span> : <span className="shimmer" aria-label="Pending" />;
      return <span className="num">{formatDuration(s.durationMs)}</span>;
    },
  },
  { id: 'time', label: 'Timestamp', sort: 'time', width: 104, min: 60, render: (s) => <span className="num muted">{formatClock(s.timestamp)}</span>, title: (s) => s.timestamp },
];

const WIDTHS_KEY = 'apkinspector.network.columns';

export function loadColumnWidths(): Record<string, number> {
  const v = loadJSON<Record<string, unknown>>(WIDTHS_KEY, {});
  const out: Record<string, number> = {};
  for (const c of COLUMNS) {
    const w = v && typeof v === 'object' ? v[c.id] : undefined;
    out[c.id] = typeof w === 'number' && Number.isFinite(w) ? Math.max(c.min, Math.min(2000, w)) : c.width;
  }
  return out;
}

export function saveColumnWidths(w: Record<string, number>): void {
  saveJSON(WIDTHS_KEY, w);
}

/** gridTemplate builds the CSS grid template; the last column absorbs spare space. */
export function gridTemplate(widths: Record<string, number>): string {
  return COLUMNS.map((c, i) => (i === COLUMNS.length - 1 ? `minmax(${widths[c.id]}px, 1fr)` : `${widths[c.id]}px`)).join(' ');
}
