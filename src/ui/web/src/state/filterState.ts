// Network filter/sort state, persisted in localStorage.
import type { QuickFilter, SortKey } from '../api/types';
import { loadJSON, saveJSON } from '../lib/storage';
import { createStore } from './store';

export const QUICK_FILTERS: { id: QuickFilter; label: string }[] = [
  { id: 'all', label: 'All' },
  { id: 'api', label: 'XHR/API' },
  { id: 'document', label: 'Documents' },
  { id: 'image', label: 'Images' },
  { id: 'media', label: 'Media' },
  { id: 'websocket', label: 'WebSocket' },
  { id: 'dns', label: 'DNS' },
  { id: 'other', label: 'Other' },
];

const SORT_KEYS: SortKey[] = ['seq', 'time', 'method', 'host', 'path', 'status', 'type', 'size', 'duration'];

export interface FilterState {
  filter: string;
  quick: QuickFilter;
  sort: SortKey;
  desc: boolean;
}

export const FILTER_STORAGE_KEY = 'apkinspector.network.filter';
export const DEFAULT_FILTER_STATE: FilterState = { filter: '', quick: 'all', sort: 'seq', desc: false };

/** sanitizeFilterState accepts only well-formed persisted values. */
export function sanitizeFilterState(v: unknown): FilterState {
  const out = { ...DEFAULT_FILTER_STATE };
  if (!v || typeof v !== 'object') return out;
  const o = v as Record<string, unknown>;
  if (typeof o.filter === 'string') out.filter = o.filter.slice(0, 2000);
  if (typeof o.quick === 'string' && QUICK_FILTERS.some((q) => q.id === o.quick)) out.quick = o.quick as QuickFilter;
  if (typeof o.sort === 'string' && SORT_KEYS.includes(o.sort as SortKey)) out.sort = o.sort as SortKey;
  if (typeof o.desc === 'boolean') out.desc = o.desc;
  return out;
}

export function loadFilterState(): FilterState {
  return sanitizeFilterState(loadJSON<unknown>(FILTER_STORAGE_KEY, null));
}

export function saveFilterState(s: FilterState): void {
  saveJSON(FILTER_STORAGE_KEY, s);
}

export const filterStore = createStore<FilterState>(loadFilterState());
filterStore.subscribe(() => saveFilterState(filterStore.get()));

/** nextSort cycles a column: ascending → descending → back to capture order. */
export function nextSort(cur: { sort: SortKey; desc: boolean }, key: SortKey): { sort: SortKey; desc: boolean } {
  if (cur.sort !== key) return { sort: key, desc: false };
  if (!cur.desc) return { sort: key, desc: true };
  return { sort: 'seq', desc: false };
}
