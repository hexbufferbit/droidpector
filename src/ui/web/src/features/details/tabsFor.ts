import type { EventDetail } from '../../api/types';

export type DetailTab = 'overview' | 'headers' | 'query' | 'request' | 'response' | 'timing' | 'connection' | 'dns' | 'messages';

export const TAB_LABELS: Record<DetailTab, string> = {
  overview: 'Overview',
  headers: 'Headers',
  query: 'Query',
  request: 'Request',
  response: 'Response',
  timing: 'Timing',
  connection: 'Connection',
  dns: 'DNS',
  messages: 'Messages',
};

/** tabsFor lists the detail tabs relevant for an event's kind. */
export function tabsFor(d: EventDetail): DetailTab[] {
  if (d.kind === 'dns') return d.conn ? ['overview', 'dns', 'connection'] : ['overview', 'dns'];
  const tabs: DetailTab[] = ['overview'];
  if (d.kind !== 'http' && d.kind !== 'websocket') {
    tabs.push('connection');
    if (d.timing) tabs.push('timing');
    return tabs;
  }
  if (d.encrypted) {
    tabs.push('connection');
    if (d.timing) tabs.push('timing');
    return tabs;
  }
  tabs.push('headers');
  if (d.kind === 'websocket') {
    tabs.push('messages');
  } else {
    tabs.push('query', 'request', 'response');
  }
  if (d.timing) tabs.push('timing');
  if (d.tls || d.conn) tabs.push('connection');
  return tabs;
}
