import type { Summary } from '../api/types';

function defaultPort(scheme: string): number {
  switch (scheme.toLowerCase()) {
    case 'http':
    case 'ws':
      return 80;
    case 'https':
    case 'wss':
      return 443;
  }
  return 0;
}

/** summaryUrl reconstructs the absolute URL of a row (mirrors model.Event.URL in Go). */
export function summaryUrl(s: Pick<Summary, 'scheme' | 'host' | 'port' | 'path' | 'query'>): string {
  if (!s.host) return '';
  let host = s.host;
  if (host.includes(':') && !host.startsWith('[')) host = `[${host}]`;
  const scheme = s.scheme ?? '';
  if (s.port && s.port !== defaultPort(scheme)) host += `:${s.port}`;
  let url = (scheme ? `${scheme}:` : '') + `//${host}${s.path || '/'}`;
  if (s.query) url += `?${s.query}`;
  return url;
}
