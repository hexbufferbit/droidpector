import { describe, expect, it, vi } from 'vitest';
import { ApiError, type EventQuery } from '../../api/client';
import type { EventPage, Summary } from '../../api/types';
import { EventPager, PAGE_SIZE } from './pager';

function row(i: number): Summary {
  return { id: `e${i}`, seq: i + 1, kind: 'http', category: 'api', state: 'complete', initiator: 'guest', timestamp: '', durationMs: 1, requestSize: 0, responseSize: 0, method: 'GET' };
}

function fakeBackend(total: number) {
  let version = 1;
  let n = total;
  const calls: EventQuery[] = [];
  const fetcher = vi.fn(async (_sid: string, q: EventQuery): Promise<EventPage> => {
    calls.push(q);
    if (q.filter === 'bad(') throw new ApiError(400, { title: 'filter error at position 4', code: 'bad_filter' });
    const rows = [];
    for (let i = q.offset; i < Math.min(n, q.offset + q.limit); i++) rows.push(row(i));
    return { rows, total: n, all: n, version };
  });
  return {
    fetcher,
    calls,
    grow(by: number) {
      n += by;
      version++;
    },
  };
}

const flush = () => new Promise((r) => setTimeout(r, 0));
const base = { sessionId: 's1', filter: '', quick: 'all' as const, sort: 'seq' as const, desc: false };

describe('EventPager', () => {
  it('loads only the pages of the visible window', async () => {
    const be = fakeBackend(100_000);
    const p = new EventPager(be.fetcher);
    p.setWindow(0, 40);
    p.setQuery(base);
    await flush();
    expect(p.getSnapshot().total).toBe(100_000);
    expect(be.calls.map((c) => c.offset)).toEqual([0]);
    p.setWindow(50_010, 50_060);
    await flush();
    expect(be.calls.map((c) => c.offset)).toEqual([0, 50_000]);
    expect(p.row(50_010)?.id).toBe('e50010');
    expect(p.row(10)?.id).toBe('e10');
    expect(be.calls.every((c) => c.limit === PAGE_SIZE)).toBe(true);
  });

  it('does not refetch cached pages', async () => {
    const be = fakeBackend(500);
    const p = new EventPager(be.fetcher);
    p.setWindow(0, 30);
    p.setQuery(base);
    await flush();
    p.setWindow(5, 35);
    await flush();
    expect(be.fetcher).toHaveBeenCalledTimes(1);
  });

  it('refreshes the visible window and sees growth on invalidate', async () => {
    const be = fakeBackend(150);
    const p = new EventPager(be.fetcher);
    p.setWindow(120, 150);
    p.setQuery(base);
    await flush();
    be.grow(60);
    p.invalidate();
    await flush();
    await flush();
    expect(p.getSnapshot().total).toBe(210);
    p.setWindow(180, 210); // the table follows the growth
    await flush();
    expect(p.row(205)?.id).toBe('e205');
  });

  it('keeps the current rows when a filter is invalid', async () => {
    const be = fakeBackend(10);
    const p = new EventPager(be.fetcher);
    p.setWindow(0, 20);
    p.setQuery(base);
    await flush();
    p.setQuery({ ...base, filter: 'bad(' });
    await flush();
    const s = p.getSnapshot();
    expect(s.filterError).toBe('filter error at position 4');
    expect(s.total).toBe(10);
    expect(p.row(0)?.id).toBe('e0');
    // Returning to a valid filter clears the error.
    p.setQuery(base);
    expect(p.getSnapshot().filterError).toBeNull();
  });

  it('resets when the session changes', async () => {
    const be = fakeBackend(10);
    const p = new EventPager(be.fetcher);
    p.setWindow(0, 20);
    p.setQuery(base);
    await flush();
    p.setQuery({ ...base, sessionId: null });
    expect(p.getSnapshot().total).toBe(0);
    expect(p.row(0)).toBeUndefined();
  });
});
