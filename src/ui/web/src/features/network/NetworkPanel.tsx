import { useCallback, useEffect, useMemo, useSyncExternalStore } from 'react';
import { appStore, eventsChanged, resynced, viewedSessionId, actions } from '../../state/app';
import { filterStore, nextSort } from '../../state/filterState';
import { useStore } from '../../state/store';
import type { SortKey } from '../../api/types';
import { FilterBar } from './FilterBar';
import { NetworkTable } from './NetworkTable';
import { EventPager } from './pager';

export function NetworkPanel({ pager, onOpenDetails }: { pager: EventPager; onOpenDetails: () => void }) {
  const sessionId = useStore(appStore, viewedSessionId);
  const selectedId = useStore(appStore, (s) => s.selectedEventId);
  const info = useStore(appStore, (s) => s.info);
  const filter = useStore(filterStore, (s) => s.filter);
  const quick = useStore(filterStore, (s) => s.quick);
  const sort = useStore(filterStore, (s) => s.sort);
  const desc = useStore(filterStore, (s) => s.desc);
  const snap = useSyncExternalStore(pager.subscribe, pager.getSnapshot);

  useEffect(() => {
    pager.setQuery({ sessionId, filter, quick, sort, desc });
  }, [pager, sessionId, filter, quick, sort, desc]);

  useEffect(() => {
    const offEvents = eventsChanged.on((sid) => {
      if (sid === pager.sessionId) pager.invalidate();
    });
    const offResync = resynced.on(() => pager.invalidate());
    return () => {
      offEvents();
      offResync();
    };
  }, [pager]);

  const onSort = useCallback((key: SortKey) => filterStore.set((s) => nextSort(s, key)), []);
  const generators = useMemo(() => info?.generators ?? [], [info]);

  return (
    <section className="network-panel" aria-label="Network">
      <FilterBar error={snap.filterError} total={snap.total} all={snap.all} />
      {snap.loadError && (
        <div className="inline-error" role="alert">
          {snap.loadError}
        </div>
      )}
      <NetworkTable
        pager={pager}
        selectedId={selectedId}
        onSelect={actions.selectEvent}
        onOpen={onOpenDetails}
        sort={sort}
        desc={desc}
        onSort={onSort}
        generators={generators}
      />
    </section>
  );
}
