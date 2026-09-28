import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, useSyncExternalStore } from 'react';
import type { GeneratorInfo, SortKey, Summary } from '../../api/types';
import { ContextMenu, type MenuItem } from '../../components/ContextMenu';
import { Illustration } from '../../components/Icon';
import { computeWindow, isAtBottom, scrollTopToReveal } from '../../lib/virtual';
import { revealEvent } from '../../state/app';
import { COLUMNS, gridTemplate, isErrorRow, loadColumnWidths, saveColumnWidths } from './columns';
import type { EventPager } from './pager';
import { rowActions } from './rowActions';

export const ROW_HEIGHT = 28;
const HEADER_HEIGHT = 30;
const OVERSCAN = 12;

export interface NetworkTableProps {
  pager: EventPager;
  selectedId: string | null;
  onSelect: (id: string | null) => void;
  onOpen: (id: string) => void;
  sort: SortKey;
  desc: boolean;
  onSort: (key: SortKey) => void;
  generators: GeneratorInfo[];
}

interface MenuState {
  x: number;
  y: number;
  row: Summary;
}

export function NetworkTable({ pager, selectedId, onSelect, onOpen, sort, desc, onSort, generators }: NetworkTableProps) {
  const snap = useSyncExternalStore(pager.subscribe, pager.getSnapshot);
  const scrollRef = useRef<HTMLDivElement>(null);
  const [scrollTop, setScrollTop] = useState(0);
  const [viewport, setViewport] = useState(600);
  const [widths, setWidths] = useState(loadColumnWidths);
  const [menu, setMenu] = useState<MenuState | null>(null);
  const follow = useRef(true);
  const selIndexRef = useRef(-1);
  const pendingSelIndex = useRef<number | null>(null);
  const revealId = useRef<string | null>(null);

  const bodyViewport = Math.max(0, viewport - HEADER_HEIGHT);
  const win = computeWindow(scrollTop, bodyViewport, ROW_HEIGHT, snap.total, OVERSCAN);

  useEffect(() => {
    pager.setWindow(win.start, win.end);
  }, [pager, win.start, win.end]);

  // Viewport size.
  useLayoutEffect(() => {
    const el = scrollRef.current;
    if (!el) return;
    const measure = () => setViewport(el.clientHeight || 600);
    measure();
    if (typeof ResizeObserver === 'undefined') return;
    const ro = new ResizeObserver(measure);
    ro.observe(el);
    return () => ro.disconnect();
  }, []);

  // Follow mode: stay at the bottom while new rows arrive, like DevTools.
  useLayoutEffect(() => {
    const el = scrollRef.current;
    if (!el || !follow.current) return;
    const target = el.scrollHeight - el.clientHeight;
    if (target > 0 && Math.abs(el.scrollTop - target) > 1) {
      el.scrollTop = target;
      setScrollTop(el.scrollTop);
    }
  }, [snap.total, viewport]);

  const onScroll = () => {
    const el = scrollRef.current;
    if (!el) return;
    follow.current = isAtBottom(el.scrollTop, el.clientHeight, el.scrollHeight, ROW_HEIGHT / 2);
    setScrollTop(el.scrollTop);
  };

  const reveal = useCallback(
    (index: number) => {
      const el = scrollRef.current;
      if (!el) return;
      const top = scrollTopToReveal(index, ROW_HEIGHT, el.scrollTop, Math.max(ROW_HEIGHT, el.clientHeight - HEADER_HEIGHT));
      if (top !== null) {
        el.scrollTop = top;
        setScrollTop(el.scrollTop);
      }
    },
    [],
  );

  // Track the selected row's index (for keyboard navigation) and apply
  // keyboard selections whose page was still loading.
  useEffect(() => {
    if (pendingSelIndex.current !== null) {
      const r = pager.row(pendingSelIndex.current);
      if (r) {
        selIndexRef.current = pendingSelIndex.current;
        pendingSelIndex.current = null;
        onSelect(r.id);
        return;
      }
    }
    if (selectedId) {
      const i = pager.indexOf(selectedId);
      if (i >= 0) selIndexRef.current = i;
    }
    if (revealId.current) {
      const i = pager.indexOf(revealId.current);
      if (i >= 0) {
        revealId.current = null;
        reveal(i);
      }
    }
  }, [snap.rev, selectedId, pager, onSelect, reveal]);

  // Reveal a newly created (replayed) request: it is appended, so jump to the end.
  useEffect(
    () =>
      revealEvent.on((id) => {
        revealId.current = id;
        const el = scrollRef.current;
        if (el && sort === 'seq' && !desc) {
          follow.current = true;
          el.scrollTop = el.scrollHeight;
        }
        pager.invalidate();
      }),
    [pager, sort, desc],
  );

  const moveSelection = (index: number) => {
    if (snap.total === 0) return;
    const i = Math.max(0, Math.min(snap.total - 1, index));
    reveal(i);
    const r = pager.row(i);
    selIndexRef.current = i;
    if (r) onSelect(r.id);
    else pendingSelIndex.current = i;
  };

  const onKeyDown = (e: React.KeyboardEvent) => {
    const cur = selectedId ? selIndexRef.current : -1;
    const pageRows = Math.max(1, Math.floor(bodyViewport / ROW_HEIGHT) - 1);
    switch (e.key) {
      case 'ArrowDown':
        moveSelection(cur < 0 ? win.start : cur + 1);
        break;
      case 'ArrowUp':
        moveSelection(cur < 0 ? win.start : cur - 1);
        break;
      case 'PageDown':
        moveSelection(cur + pageRows);
        break;
      case 'PageUp':
        moveSelection(cur - pageRows);
        break;
      case 'Home':
        moveSelection(0);
        break;
      case 'End':
        follow.current = true;
        moveSelection(snap.total - 1);
        break;
      case 'Enter':
        if (selectedId) onOpen(selectedId);
        break;
      case 'Escape':
        onSelect(null);
        break;
      case 'ContextMenu': {
        const r = cur >= 0 ? pager.row(cur) : undefined;
        const el = scrollRef.current?.querySelector<HTMLElement>(`[data-index="${cur}"]`);
        if (r && el) {
          const rect = el.getBoundingClientRect();
          setMenu({ x: rect.left + 40, y: rect.bottom, row: r });
        }
        break;
      }
      default:
        return;
    }
    e.preventDefault();
  };

  // Column resizing.
  const startResize = (e: React.PointerEvent, colId: string) => {
    e.preventDefault();
    e.stopPropagation();
    const startX = e.clientX;
    const startW = widths[colId];
    const min = COLUMNS.find((c) => c.id === colId)?.min ?? 40;
    let latest = widths;
    document.body.classList.add('resizing-col');
    const move = (ev: PointerEvent) => {
      latest = { ...latest, [colId]: Math.max(min, startW + ev.clientX - startX) };
      setWidths(latest);
    };
    const up = () => {
      document.body.classList.remove('resizing-col');
      window.removeEventListener('pointermove', move);
      window.removeEventListener('pointerup', up);
      saveColumnWidths(latest);
    };
    window.addEventListener('pointermove', move);
    window.addEventListener('pointerup', up);
  };

  const template = useMemo(() => gridTemplate(widths), [widths]);
  const minWidth = useMemo(() => Object.values(widths).reduce((a, b) => a + b, 0), [widths]);

  const rows: React.ReactNode[] = [];
  for (let i = win.start; i < win.end; i++) {
    const r = pager.row(i);
    const style = { transform: `translateY(${i * ROW_HEIGHT}px)` };
    if (!r) {
      rows.push(
        <div key={`p${i}`} role="row" aria-rowindex={i + 2} className="net-row placeholder" style={style} data-index={i}>
          <div role="gridcell" className="net-cell" />
        </div>,
      );
      continue;
    }
    const cls = ['net-row'];
    if (r.id === selectedId) cls.push('selected');
    if (isErrorRow(r)) cls.push('error');
    if (r.state === 'pending') cls.push('pending');
    if (r.encrypted) cls.push('encrypted');
    rows.push(
      <div
        key={r.id}
        role="row"
        aria-rowindex={i + 2}
        aria-selected={r.id === selectedId}
        className={cls.join(' ')}
        style={style}
        data-index={i}
        data-id={r.id}
        data-initiator={r.initiator}
        onMouseDown={(e) => {
          if (e.button !== 0) return;
          selIndexRef.current = i;
          onSelect(r.id);
        }}
        onDoubleClick={() => onOpen(r.id)}
        onContextMenu={(e) => {
          e.preventDefault();
          selIndexRef.current = i;
          onSelect(r.id);
          setMenu({ x: e.clientX, y: e.clientY, row: r });
        }}
      >
        {COLUMNS.map((c) => (
          <div key={c.id} role="gridcell" className={`net-cell col-${c.id}${c.align === 'right' ? ' right' : ''}`} title={c.title?.(r)}>
            {c.render(r)}
          </div>
        ))}
      </div>,
    );
  }

  const menuItems = menu ? buildMenu(menu.row, generators) : [];

  return (
    <>
      <div
        ref={scrollRef}
        className="net-table"
        role="grid"
        aria-label="Network requests"
        aria-rowcount={snap.total + 1}
        aria-colcount={COLUMNS.length}
        tabIndex={0}
        onScroll={onScroll}
        onKeyDown={onKeyDown}
        style={{ ['--net-cols' as string]: template }}
      >
        <div className="net-head" role="row" aria-rowindex={1} style={{ minWidth }}>
          {COLUMNS.map((c) => {
            const active = sort === c.sort;
            return (
              <div
                key={c.id}
                role="columnheader"
                aria-sort={active ? (desc ? 'descending' : 'ascending') : 'none'}
                className={`net-hcell${c.align === 'right' ? ' right' : ''}${active ? ' sorted' : ''}`}
              >
                <button className="net-sort" onClick={() => onSort(c.sort)} title={`Sort by ${c.label}`}>
                  <span className="net-sort-label">{c.label}</span>
                  {active && (
                    <span className="sort-arrow" aria-hidden="true">
                      {desc ? '↓' : '↑'}
                    </span>
                  )}
                </button>
                <span
                  className="col-resize"
                  role="separator"
                  aria-orientation="vertical"
                  aria-label={`Resize ${c.label} column`}
                  onPointerDown={(e) => startResize(e, c.id)}
                  onDoubleClick={() => {
                    const w = { ...widths, [c.id]: c.width };
                    setWidths(w);
                    saveColumnWidths(w);
                  }}
                />
              </div>
            );
          })}
        </div>
        <div className="net-body" style={{ height: win.totalHeight, minWidth }} role="rowgroup">
          {rows}
        </div>
        {snap.loaded && snap.total === 0 && (
          <div className="net-empty empty-state">
            <Illustration name={snap.all > 0 ? 'filter' : 'globe'} />
            <p className="empty-title">{snap.all > 0 ? 'Nothing matches this filter' : 'No requests yet'}</p>
            <p className="empty-sub">
              {snap.all > 0
                ? 'Loosen the filter or pick “All” above to see every request of this session.'
                : 'Install an APK and use the app: every request it makes shows up here as it happens.'}
            </p>
          </div>
        )}
      </div>
      {menu && <ContextMenu x={menu.x} y={menu.y} items={menuItems} onClose={() => setMenu(null)} label="Request actions" />}
    </>
  );
}

function buildMenu(row: Summary, generators: GeneratorInfo[]): MenuItem[] {
  const http = row.kind === 'http' || row.kind === 'websocket';
  const inspectable = http && !row.encrypted;
  const replayable = row.kind === 'http' && !row.encrypted && !!row.method;
  const gens = generators.length ? generators : [{ id: 'curl', label: 'Copy as cURL' }];
  const items: MenuItem[] = gens.map((g) => ({
    id: `gen-${g.id}`,
    label: g.label,
    disabled: !inspectable,
    onSelect: () => void rowActions.copyCode(row.id, g),
  }));
  items.push(
    { id: 'copy-url', label: 'Copy URL', disabled: !row.host, onSelect: () => void rowActions.copyUrl(row) },
    { id: 'copy-headers', label: 'Copy Headers', disabled: !inspectable, onSelect: () => void rowActions.copyHeaders(row.id) },
    { id: 'copy-response', label: 'Copy Response', disabled: !inspectable || row.kind !== 'http', onSelect: () => void rowActions.copyResponse(row.id) },
    {
      id: 'replay',
      label: 'Replay',
      separatorBefore: true,
      disabled: !replayable,
      title: replayable ? 'Send this request again' : 'Only inspected HTTP requests can be replayed',
      onSelect: () => void rowActions.replay(row.id),
    },
    { id: 'save-har', label: 'Save Request (HAR)', disabled: !http, onSelect: () => rowActions.saveHar(row.id) },
    { id: 'filter-host', label: `Filter by this host`, separatorBefore: true, disabled: !row.host, onSelect: () => rowActions.filterByHost(row.host ?? '') },
  );
  return items;
}
