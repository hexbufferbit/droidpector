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

export function typeLabel(s: Summary): string {
  switch (s.kind) {
    case 'dns':
      return 'dns';
    case 'websocket':
      return 'websocket';
    case 'tls':
      return 'tls';
    case 'tcp':
      return 'tcp';
    case 'udp':
      return 'udp';
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

export const COLUMNS: Column[] = [
  { id: 'method', label: 'Method', sort: 'method', width: 72, min: 48, render: (s) => s.method || s.protocol || '' },
  {
    id: 'host',
    label: 'Host',
    sort: 'host',
    width: 180,
    min: 60,
    render: (s) => (
      <>
        {s.initiator === 'replay' && (
          <span className="replay-icon" title="Replayed request" aria-label="Replayed request">
            <Icon name="replay" size={12} />
          </span>
        )}
        {s.host}
      </>
    ),
    title: (s) => s.host ?? '',
  },
  {
    id: 'path',
    label: 'Path',
    sort: 'path',
    width: 260,
    min: 80,
    render: (s) => (
      <>
        {s.encrypted && (
          <span className="badge encrypted" title="HTTPS encrypted traffic: payload inspection unavailable">
            <Icon name="lock" size={11} /> encrypted
          </span>
        )}
        {s.path}
        {s.query ? <span className="query">?{s.query}</span> : null}
      </>
    ),
    title: (s) => (s.path ?? '') + (s.query ? `?${s.query}` : ''),
  },
  { id: 'status', label: 'Status', sort: 'status', width: 70, min: 48, render: statusText, title: (s) => s.error || statusText(s) },
  { id: 'type', label: 'Type', sort: 'type', width: 84, min: 48, render: typeLabel, title: (s) => s.mime || s.category },
  {
    id: 'size',
    label: 'Size',
    sort: 'size',
    width: 72,
    min: 48,
    align: 'right',
    render: (s) => (s.kind === 'dns' ? '' : formatBytes(s.responseSize)),
    title: (s) => `Request ${formatBytes(s.requestSize)} · Response ${formatBytes(s.responseSize)}`,
  },
  {
    id: 'duration',
    label: 'Duration',
    sort: 'duration',
    width: 76,
    min: 48,
    align: 'right',
    render: (s) => (s.state === 'pending' ? 'pending' : formatDuration(s.durationMs)),
  },
  { id: 'time', label: 'Timestamp', sort: 'time', width: 104, min: 60, render: (s) => formatClock(s.timestamp), title: (s) => s.timestamp },
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
