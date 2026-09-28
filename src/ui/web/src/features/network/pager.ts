// EventPager keeps the page cache of the virtualized Network table. It only
// ever requests the visible window (+overscan) from the Network Query Service;
// the full list never lives in the UI.
import { api, toApiError } from '../../api/client';
import type { QuickFilter, SortKey, Summary } from '../../api/types';
import { pagesFor } from '../../lib/virtual';

export const PAGE_SIZE = 100;

export interface PagerQuery {
  sessionId: string | null;
  filter: string;
  quick: QuickFilter;
  sort: SortKey;
  desc: boolean;
}

export function queryKey(q: PagerQuery): string {
  return JSON.stringify([q.sessionId, q.filter.trim(), q.quick, q.sort, q.desc]);
}

interface Page {
  rows: Summary[];
  version: number;
}

export interface PagerSnapshot {
  rev: number;
  total: number;
  all: number;
  /** message of an invalid filter (bad_filter) */
  filterError: string | null;
  /** any other loading problem */
  loadError: string | null;
  loaded: boolean;
}

type Fetcher = typeof api.events;

export class EventPager {
  private query: PagerQuery | null = null;
  private key = '';
  private pendingKey: string | null = null;
  private pendingQuery: PagerQuery | null = null;
  private pages = new Map<number, Page>();
  private inflight = new Set<string>();
  private version = -1;
  private start = 0;
  private end = 0;
  private refreshing = false;
  private refreshAgain = false;
  private listeners = new Set<() => void>();
  private snap: PagerSnapshot = { rev: 0, total: 0, all: 0, filterError: null, loadError: null, loaded: false };
  private readonly fetcher: Fetcher;

  constructor(fetcher: Fetcher = api.events) {
    this.fetcher = fetcher;
  }

  subscribe = (fn: () => void): (() => void) => {
    this.listeners.add(fn);
    return () => this.listeners.delete(fn);
  };

  getSnapshot = (): PagerSnapshot => this.snap;

  private emit(patch: Partial<PagerSnapshot> = {}): void {
    this.snap = { ...this.snap, ...patch, rev: this.snap.rev + 1 };
    for (const fn of [...this.listeners]) fn();
  }

  get sessionId(): string | null {
    return this.query?.sessionId ?? null;
  }

  /** row returns the cached row at index, or undefined while it loads. */
  row(index: number): Summary | undefined {
    const p = this.pages.get(Math.floor(index / PAGE_SIZE));
    return p?.rows[index % PAGE_SIZE];
  }

  /** indexOf finds a cached row by id. */
  indexOf(id: string): number {
    for (const [pi, p] of this.pages) {
      const i = p.rows.findIndex((r) => r.id === id);
      if (i >= 0) return pi * PAGE_SIZE + i;
    }
    return -1;
  }

  setQuery(q: PagerQuery): void {
    const k = queryKey(q);
    if (k === this.key && this.query) {
      this.pendingKey = null;
      this.pendingQuery = null;
      if (this.snap.filterError) this.emit({ filterError: null });
      return;
    }
    if (k === this.pendingKey) return;
    if (!q.sessionId) {
      this.query = q;
      this.key = k;
      this.pendingKey = null;
      this.pendingQuery = null;
      this.pages.clear();
      this.version = -1;
      this.emit({ total: 0, all: 0, filterError: null, loadError: null, loaded: true });
      return;
    }
    // A session change resets immediately; a filter/sort change keeps showing
    // the old rows until the new query succeeds (an invalid filter never clears the list).
    if (!this.query || this.query.sessionId !== q.sessionId) {
      this.query = q;
      this.key = k;
      this.pendingKey = null;
      this.pendingQuery = null;
      this.pages.clear();
      this.version = -1;
      this.emit({ total: 0, all: 0, filterError: null, loadError: null, loaded: false });
      this.ensure(true);
      return;
    }
    this.pendingKey = k;
    this.pendingQuery = q;
    void this.load(q, k, this.visiblePages().length ? this.visiblePages() : [0], true);
  }

  setWindow(start: number, end: number): void {
    if (start === this.start && end === this.end) return;
    this.start = start;
    this.end = end;
    this.ensure(false);
  }

  /** invalidate refetches the visible window after the session's events changed. */
  invalidate(): void {
    if (!this.query?.sessionId) return;
    if (this.refreshing) {
      this.refreshAgain = true;
      return;
    }
    this.refreshing = true;
    const q = this.query;
    const k = this.key;
    const pages = this.visiblePages();
    // Always include the page that follows the current total, so growth is seen.
    const tail = Math.floor(this.snap.total / PAGE_SIZE);
    if (!pages.includes(tail) && (pages.length === 0 || pages[pages.length - 1] + 1 === tail)) pages.push(tail);
    void this.load(q, k, pages.length ? pages : [0], true).finally(() => {
      this.refreshing = false;
      if (this.refreshAgain) {
        this.refreshAgain = false;
        this.invalidate();
      }
    });
  }

  private visiblePages(): number[] {
    return pagesFor(this.start, Math.max(this.end, this.start + 1), PAGE_SIZE);
  }

  private ensure(force: boolean): void {
    if (!this.query?.sessionId) return;
    const pages = this.visiblePages().filter((p) => force || !this.pages.has(p));
    if (pages.length === 0 && this.snap.loaded) return;
    void this.load(this.query, this.key, pages.length ? pages : [0], force);
  }

  private async load(q: PagerQuery, k: string, pages: number[], force: boolean): Promise<void> {
    const tasks = pages.map(async (p) => {
      const id = `${k}#${p}`;
      if (!force && this.inflight.has(id)) return;
      this.inflight.add(id);
      try {
        const res = await this.fetcher(q.sessionId as string, {
          filter: q.filter.trim(),
          quick: q.quick,
          sort: q.sort,
          desc: q.desc,
          offset: p * PAGE_SIZE,
          limit: PAGE_SIZE,
        });
        this.accept(k, q, p, res.rows ?? [], res.total, res.all, res.version);
      } catch (err) {
        this.reject(k, err);
      } finally {
        this.inflight.delete(id);
      }
    });
    await Promise.all(tasks);
  }

  private accept(k: string, q: PagerQuery, page: number, rows: Summary[], total: number, all: number, version: number): void {
    if (k === this.pendingKey) {
      // The new filter/sort is valid: switch to it.
      this.query = this.pendingQuery ?? q;
      this.key = k;
      this.pendingKey = null;
      this.pendingQuery = null;
      this.pages.clear();
      this.version = -1;
    } else if (k !== this.key) {
      return; // stale response of an abandoned query
    }
    if (version > this.version) {
      // Rows may have shifted: keep only visible pages (they are refreshed).
      const visible = new Set(this.visiblePages());
      for (const p of [...this.pages.keys()]) if (!visible.has(p)) this.pages.delete(p);
      this.version = version;
      let stale = false;
      for (const [p, pg] of this.pages) if (p !== page && pg.version < version) stale = true;
      if (stale) queueMicrotask(() => this.invalidate());
    }
    this.pages.set(page, { rows, version });
    for (const p of [...this.pages.keys()]) if (p * PAGE_SIZE >= total && p !== 0) this.pages.delete(p);
    this.emit({ total, all, filterError: null, loadError: null, loaded: true });
  }

  private reject(k: string, err: unknown): void {
    if (err instanceof DOMException && err.name === 'AbortError') return;
    const e = toApiError(err);
    if (k === this.pendingKey) {
      this.pendingKey = null;
      this.pendingQuery = null;
      if (e.code === 'bad_filter') {
        this.emit({ filterError: e.title });
        return;
      }
    }
    if (k !== this.key) return;
    if (e.code === 'bad_filter') this.emit({ filterError: e.title, loaded: true });
    else if (e.code === 'not_found') this.emit({ loadError: 'This session no longer exists.', total: 0, loaded: true });
    else this.emit({ loadError: e.title, loaded: true });
  }
}
