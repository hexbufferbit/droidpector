import { memo, useMemo, useState } from 'react';
import { Icon } from '../../../components/Icon';
import {
  childPath,
  containerPaths,
  defaultExpanded,
  entriesOf,
  isContainer,
  primitiveText,
  searchJson,
  splitHighlight,
  summarize,
  type Json,
} from '../../../lib/jsonTree';

const CHILD_LIMIT = 500;

function Highlight({ text, query }: { text: string; query: string }) {
  if (!query) return <>{text}</>;
  return (
    <>
      {splitHighlight(text, query).map((seg, i) => (seg.hit ? <mark key={i}>{seg.text}</mark> : <span key={i}>{seg.text}</span>))}
    </>
  );
}

interface NodeCtx {
  expanded: Set<string>;
  toggle: (path: string) => void;
  query: string;
  matches: Set<string>;
  showAll: Set<string>;
  setShowAll: (path: string) => void;
}

function Primitive({ v, query }: { v: Json; query: string }) {
  const cls = v === null ? 'j-null' : typeof v === 'string' ? 'j-str' : typeof v === 'number' ? 'j-num' : 'j-bool';
  const text = typeof v === 'string' ? JSON.stringify(v) : primitiveText(v);
  return (
    <span className={cls}>
      <Highlight text={text} query={query} />
    </span>
  );
}

const JsonNode = memo(function JsonNode({ name, value, path, ctx, depth }: { name: string | null; value: Json; path: string; ctx: NodeCtx; depth: number }) {
  const hit = ctx.matches.has(path);
  const key =
    name !== null ? (
      <>
        <span className="j-key">
          <Highlight text={name} query={ctx.query} />
        </span>
        <span className="j-punct">: </span>
      </>
    ) : null;
  if (!isContainer(value)) {
    return (
      <div role="treeitem" aria-level={depth + 1} className={`j-row${hit ? ' j-hit' : ''}`} style={{ paddingLeft: depth * 14 + 16 }}>
        {key}
        <Primitive v={value} query={ctx.query} />
      </div>
    );
  }
  const open = ctx.expanded.has(path);
  const entries = open ? entriesOf(value) : [];
  const shown = ctx.showAll.has(path) ? entries : entries.slice(0, CHILD_LIMIT);
  const arr = Array.isArray(value);
  return (
    <div role="treeitem" aria-level={depth + 1} aria-expanded={open} className="j-node">
      <div className={`j-row${hit ? ' j-hit' : ''}`} style={{ paddingLeft: depth * 14 }} onClick={() => ctx.toggle(path)}>
        <button className="j-toggle" aria-label={open ? 'Collapse' : 'Expand'} tabIndex={-1}>
          <Icon name={open ? 'chevronDown' : 'chevronRight'} size={10} />
        </button>
        {key}
        <span className="j-punct">{arr ? '[' : '{'}</span>
        {!open && (
          <>
            <span className="j-summary">{summarize(value)}</span>
            <span className="j-punct">{arr ? ']' : '}'}</span>
          </>
        )}
      </div>
      {open && (
        <div role="group">
          {shown.map(([k, v]) => (
            <JsonNode key={k} name={arr ? null : k} value={v} path={childPath(path, k)} ctx={ctx} depth={depth + 1} />
          ))}
          {shown.length < entries.length && (
            <div className="j-row" style={{ paddingLeft: (depth + 1) * 14 + 16 }}>
              <button className="link-btn" onClick={() => ctx.setShowAll(path)}>
                Show {entries.length - shown.length} more…
              </button>
            </div>
          )}
          <div className="j-row" style={{ paddingLeft: depth * 14 + 16 }}>
            <span className="j-punct">{arr ? ']' : '}'}</span>
          </div>
        </div>
      )}
    </div>
  );
});

/** JsonView is a collapsible JSON tree with expand/collapse all and search. */
export function JsonView({ value }: { value: Json }) {
  const [expanded, setExpanded] = useState(() => defaultExpanded(value, 2));
  const [query, setQuery] = useState('');
  const [showAll, setShowAllState] = useState(() => new Set<string>());
  const result = useMemo(() => searchJson(value, query), [value, query]);

  const effective = useMemo(() => {
    if (result.expand.size === 0) return expanded;
    const s = new Set(expanded);
    for (const p of result.expand) s.add(p);
    return s;
  }, [expanded, result]);

  const ctx: NodeCtx = useMemo(
    () => ({
      expanded: effective,
      toggle: (p: string) =>
        setExpanded((prev) => {
          const s = new Set(prev);
          for (const x of result.expand) s.add(x);
          if (effective.has(p)) s.delete(p);
          else s.add(p);
          return s;
        }),
      query: query.trim(),
      matches: new Set(result.matches),
      showAll,
      setShowAll: (p: string) => setShowAllState((prev) => new Set(prev).add(p)),
    }),
    [effective, query, result, showAll],
  );

  return (
    <div className="json-view">
      <div className="viewer-toolbar">
        <button className="btn small" onClick={() => setExpanded(new Set(containerPaths(value)))}>
          Expand all
        </button>
        <button className="btn small" onClick={() => setExpanded(new Set())}>
          Collapse all
        </button>
        <div className="search-box">
          <Icon name="search" size={12} />
          <input type="search" placeholder="Search JSON" aria-label="Search JSON" value={query} onChange={(e) => setQuery(e.target.value)} />
        </div>
        {query.trim() && (
          <span className="muted" aria-live="polite">
            {result.matches.length}
            {result.limited ? '+' : ''} {result.matches.length === 1 ? 'match' : 'matches'}
          </span>
        )}
      </div>
      <div className="json-tree mono" role="tree" aria-label="JSON">
        <JsonNode name={null} value={value} path="" ctx={ctx} depth={0} />
      </div>
    </div>
  );
}
