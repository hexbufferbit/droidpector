// Pure logic of the JSON tree viewer: paths, expansion and search.
//
// Nodes are addressed by JSON Pointer strings (RFC 6901): "" is the root,
// "/items/0/name" a nested member.

export type Json = null | boolean | number | string | Json[] | { [key: string]: Json };

export function escapeToken(key: string): string {
  return key.replace(/~/g, '~0').replace(/\//g, '~1');
}

export function childPath(parent: string, key: string | number): string {
  return `${parent}/${escapeToken(String(key))}`;
}

/** ancestors returns the container paths above path, root first (excluding path itself). */
export function ancestors(path: string): string[] {
  if (path === '') return [];
  const parts = path.split('/').slice(1);
  const out = [''];
  let cur = '';
  for (let i = 0; i < parts.length - 1; i++) {
    cur += '/' + parts[i];
    out.push(cur);
  }
  return out;
}

export function isContainer(v: unknown): v is Json[] | { [key: string]: Json } {
  return v !== null && typeof v === 'object';
}

export function entriesOf(v: Json[] | { [key: string]: Json }): [string, Json][] {
  return Array.isArray(v) ? v.map((x, i) => [String(i), x]) : Object.entries(v);
}

/** containerPaths lists every object/array path (for "expand all"), up to limit paths. */
export function containerPaths(value: Json, limit = 50_000): string[] {
  const out: string[] = [];
  const walk = (v: Json, path: string) => {
    if (!isContainer(v) || out.length >= limit) return;
    out.push(path);
    for (const [k, c] of entriesOf(v)) walk(c, childPath(path, k));
  };
  walk(value, '');
  return out;
}

/** defaultExpanded expands containers down to the given depth (root = depth 0). */
export function defaultExpanded(value: Json, depth = 1): Set<string> {
  const out = new Set<string>();
  const walk = (v: Json, path: string, d: number) => {
    if (!isContainer(v) || d > depth) return;
    out.add(path);
    for (const [k, c] of entriesOf(v)) walk(c, childPath(path, k), d + 1);
  };
  walk(value, '', 0);
  return out;
}

export interface JsonSearchResult {
  /** paths of nodes whose key or primitive value matches */
  matches: string[];
  /** containers to expand so every match is visible */
  expand: Set<string>;
  /** true when the match limit was hit */
  limited: boolean;
}

export function primitiveText(v: Json): string {
  if (v === null) return 'null';
  if (typeof v === 'string') return v;
  return String(v);
}

/** searchJson finds keys and primitive values containing query (case-insensitive). */
export function searchJson(value: Json, query: string, limit = 5_000): JsonSearchResult {
  const q = query.trim().toLowerCase();
  const matches: string[] = [];
  const expand = new Set<string>();
  if (!q) return { matches, expand, limited: false };
  let limited = false;
  const walk = (v: Json, path: string, key: string | null) => {
    if (limited) return;
    const keyHit = key !== null && key.toLowerCase().includes(q);
    const valHit = !isContainer(v) && primitiveText(v).toLowerCase().includes(q);
    if (keyHit || valHit) {
      if (matches.length >= limit) {
        limited = true;
        return;
      }
      matches.push(path);
      for (const a of ancestors(path)) expand.add(a);
    }
    if (isContainer(v)) {
      const arr = Array.isArray(v);
      for (const [k, c] of entriesOf(v)) walk(c, childPath(path, k), arr ? null : k);
    }
  };
  walk(value, '', null);
  return { matches, expand, limited };
}

/** summarize describes a container for its collapsed form: "{3}" / "[10]". */
export function summarize(v: Json[] | { [key: string]: Json }): string {
  return Array.isArray(v) ? `Array(${v.length})` : `{${Object.keys(v).length}}`;
}

/** splitHighlight splits text into [plain, match, plain, match, ...] segments for query. */
export function splitHighlight(text: string, query: string): { text: string; hit: boolean }[] {
  const q = query.trim().toLowerCase();
  if (!q) return [{ text, hit: false }];
  const lower = text.toLowerCase();
  const out: { text: string; hit: boolean }[] = [];
  let i = 0;
  for (;;) {
    const j = lower.indexOf(q, i);
    if (j < 0) break;
    if (j > i) out.push({ text: text.slice(i, j), hit: false });
    out.push({ text: text.slice(j, j + q.length), hit: true });
    i = j + q.length;
  }
  if (i < text.length || out.length === 0) out.push({ text: text.slice(i), hit: false });
  return out;
}
