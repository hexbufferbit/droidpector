// Human formatting of sizes, durations and times (DevTools conventions).

/** formatBytes formats a byte count like Chrome DevTools: B, kB, MB, GB (base 1000). */
export function formatBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes < 0) return '—';
  if (bytes < 1000) return `${Math.round(bytes)} B`;
  const kb = bytes / 1000;
  if (kb < 100) return `${kb.toFixed(1)} kB`;
  if (kb < 1000) return `${Math.round(kb)} kB`;
  const mb = kb / 1000;
  if (mb < 100) return `${mb.toFixed(1)} MB`;
  if (mb < 1000) return `${Math.round(mb)} MB`;
  const gb = mb / 1000;
  return gb < 100 ? `${gb.toFixed(1)} GB` : `${Math.round(gb)} GB`;
}

/** formatBytesExact formats with thousands separators: "12,345 B". */
export function formatBytesExact(bytes: number): string {
  return `${Math.round(bytes).toLocaleString('en-US')} B`;
}

/** formatDuration formats milliseconds: "0.36 ms", "12.4 ms", "245 ms", "1.25 s", "2.5 min". Negative = n/a. */
export function formatDuration(ms: number): string {
  if (!Number.isFinite(ms) || ms < 0) return 'n/a';
  if (ms === 0) return '0 ms';
  if (ms < 1) return `${ms.toFixed(2)} ms`;
  if (ms < 100) return `${trimZero(ms.toFixed(1))} ms`;
  if (ms < 1000) return `${Math.round(ms)} ms`;
  const s = ms / 1000;
  if (s < 60) return `${trimZero(s.toFixed(2))} s`;
  const min = s / 60;
  if (min < 60) return `${trimZero(min.toFixed(1))} min`;
  return `${trimZero((min / 60).toFixed(1))} h`;
}

function trimZero(s: string): string {
  return s.includes('.') ? s.replace(/\.?0+$/, '') : s;
}

/** formatElapsed formats a session length: "45s", "12m", "1h 5m", "2d 3h". */
export function formatElapsed(ms: number): string {
  if (!Number.isFinite(ms) || ms < 0) ms = 0;
  const s = Math.floor(ms / 1000);
  if (s < 60) return `${s}s`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m`;
  const h = Math.floor(m / 60);
  if (h < 24) return m % 60 ? `${h}h ${m % 60}m` : `${h}h`;
  const d = Math.floor(h / 24);
  return h % 24 ? `${d}d ${h % 24}h` : `${d}d`;
}

function pad(n: number, w = 2): string {
  return String(n).padStart(w, '0');
}

/** formatClock formats a timestamp as local "HH:MM:SS.mmm". */
export function formatClock(ts: string | Date): string {
  const d = typeof ts === 'string' ? new Date(ts) : ts;
  if (Number.isNaN(d.getTime())) return '';
  return `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}.${pad(d.getMilliseconds(), 3)}`;
}

/** formatDateTime formats a timestamp as local "YYYY-MM-DD HH:MM:SS.mmm". */
export function formatDateTime(ts: string | Date): string {
  const d = typeof ts === 'string' ? new Date(ts) : ts;
  if (Number.isNaN(d.getTime())) return '';
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${formatClock(d)}`;
}

/** pluralize("request", 3) → "3 requests". */
export function pluralize(word: string, n: number): string {
  return `${n.toLocaleString('en-US')} ${word}${n === 1 ? '' : 's'}`;
}
