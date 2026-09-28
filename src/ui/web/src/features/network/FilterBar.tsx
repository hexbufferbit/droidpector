import { useEffect, useId, useRef, useState } from 'react';
import { Icon } from '../../components/Icon';
import { filterStore, QUICK_FILTERS } from '../../state/filterState';
import { useStore } from '../../state/store';
import { pluralize } from '../../lib/format';

export const FILTER_DEBOUNCE_MS = 200;

const SYNTAX: [string, string][] = [
  ['api.example.com', 'Free text: part of the URL'],
  ['host:api.example.com', 'Host (exact, or glob like *.example.com)'],
  ['host contains "example"', 'Operators: contains, startswith, endswith, equals, matches'],
  ['method:POST', 'HTTP method'],
  ['status:404  status:4xx  status>=400', 'Status code, class or comparison'],
  ['path:/v1/', 'Path contains'],
  ['scheme:https', 'Scheme (http, https, ws, wss)'],
  ['mime:json  content-type:image', 'Response content type contains'],
  ['type:image', 'Category: api, document, image, media, websocket, dns, other'],
  ['size>10k  reqsize>1mb', 'Response / request size (b, k, kb, m, mb)'],
  ['duration>500ms  duration>=2s', 'Duration'],
  ['is:error  is:pending  is:encrypted  is:replay', 'Flags'],
  ['-host:cdn.example.com   NOT method:GET', 'Negation'],
  ['method:GET OR method:HEAD', 'Either condition (terms are AND-ed by default)'],
  ['(status:4xx OR status:5xx) host:api', 'Grouping with parentheses'],
  ['path:/^\\/v[0-9]+\\//', 'Regular expression between slashes'],
];

export function FilterHelp({ onClose }: { onClose: () => void }) {
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const onDown = (e: MouseEvent) => {
      const t = e.target as HTMLElement;
      if (!ref.current?.contains(t) && !t.closest('.filter-help-btn')) onClose();
    };
    const onKey = (e: KeyboardEvent) => e.key === 'Escape' && onClose();
    window.addEventListener('mousedown', onDown);
    window.addEventListener('keydown', onKey);
    return () => {
      window.removeEventListener('mousedown', onDown);
      window.removeEventListener('keydown', onKey);
    };
  }, [onClose]);
  return (
    <div className="popover filter-help" role="dialog" aria-label="Filter syntax" ref={ref}>
      <h3>Filter syntax</h3>
      <table>
        <tbody>
          {SYNTAX.map(([ex, desc]) => (
            <tr key={ex}>
              <td>
                <code>{ex}</code>
              </td>
              <td>{desc}</td>
            </tr>
          ))}
        </tbody>
      </table>
      <p className="muted">Filters are case-insensitive. Invalid filters are highlighted and keep the current list.</p>
    </div>
  );
}

export function FilterBar({ error, total, all }: { error: string | null; total: number; all: number }) {
  const stored = useStore(filterStore, (s) => s.filter);
  const quick = useStore(filterStore, (s) => s.quick);
  const [text, setText] = useState(stored);
  const [help, setHelp] = useState(false);
  const errorId = useId();
  const lastPushed = useRef(stored);

  // External changes (e.g. "Filter by this host") update the box.
  useEffect(() => {
    if (stored !== lastPushed.current) {
      lastPushed.current = stored;
      setText(stored);
    }
  }, [stored]);

  useEffect(() => {
    if (text === lastPushed.current) return;
    const t = setTimeout(() => {
      lastPushed.current = text;
      filterStore.set({ filter: text });
    }, FILTER_DEBOUNCE_MS);
    return () => clearTimeout(t);
  }, [text]);

  return (
    <div className="filter-bar">
      <div className="filter-row">
        <div className={`filter-input${error ? ' invalid' : ''}`}>
          <Icon name="filter" />
          <input
            type="search"
            placeholder="Filter (e.g. host:api.example.com method:POST status:4xx)"
            aria-label="Filter requests"
            aria-invalid={!!error}
            aria-describedby={error ? errorId : undefined}
            spellCheck={false}
            value={text}
            onChange={(e) => setText(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter') {
                lastPushed.current = text;
                filterStore.set({ filter: text });
              } else if (e.key === 'Escape' && text) {
                e.stopPropagation();
                setText('');
              }
            }}
          />
          {text && (
            <button className="icon-btn small" aria-label="Clear filter" title="Clear filter" onClick={() => setText('')}>
              <Icon name="close" size={12} />
            </button>
          )}
        </div>
        <button
          className="icon-btn filter-help-btn"
          aria-label="Filter syntax help"
          title="Filter syntax"
          aria-expanded={help}
          onClick={() => setHelp(!help)}
        >
          <Icon name="help" />
        </button>
        <span className="net-count" aria-live="polite">
          {total === all ? pluralize('request', all) : `${total.toLocaleString('en-US')} / ${pluralize('request', all)}`}
        </span>
        {help && <FilterHelp onClose={() => setHelp(false)} />}
      </div>
      {error && (
        <div className="filter-error" id={errorId} role="alert">
          {error}
        </div>
      )}
      <div className="quick-filters" role="toolbar" aria-label="Quick filters">
        {QUICK_FILTERS.map((q) => (
          <button key={q.id} className={`chip${quick === q.id ? ' active' : ''}`} aria-pressed={quick === q.id} onClick={() => filterStore.set({ quick: q.id })}>
            {q.label}
          </button>
        ))}
      </div>
    </div>
  );
}
