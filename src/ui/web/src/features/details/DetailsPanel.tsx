import { useCallback, useEffect, useState } from 'react';
import { api, toApiError } from '../../api/client';
import type { ErrorInfo, EventDetail } from '../../api/types';
import { ErrorView } from '../../components/ErrorView';
import { Icon } from '../../components/Icon';
import { Tabs } from '../../components/Tabs';
import { loadJSON, saveJSON } from '../../lib/storage';
import { summaryUrl } from '../../lib/url';
import { actions, appStore, eventsChanged } from '../../state/app';
import { useStore } from '../../state/store';
import { rowActions } from '../network/rowActions';
import { BodyViewer } from './body/BodyViewer';
import { EncryptedNotice } from './EncryptedNotice';
import { HeadersTab } from './HeadersTab';
import { ConnectionTab, DnsTab, QueryTab, TimingTab } from './InfoTabs';
import { MessagesTab } from './MessagesTab';
import { OverviewTab } from './OverviewTab';
import { TAB_LABELS, tabsFor, type DetailTab } from './tabsFor';

const TAB_KEY = 'apkinspector.details.tab';

function useEventDetail(id: string | null) {
  const [detail, setDetail] = useState<EventDetail | null>(null);
  const [error, setError] = useState<ErrorInfo | null>(null);
  const [nonce, setNonce] = useState(0);

  useEffect(() => {
    setDetail((d) => (d?.id === id ? d : null));
    setError(null);
    if (!id) return;
    const ctl = new AbortController();
    api.event(id, ctl.signal).then(
      (d) => setDetail(d),
      (err) => {
        if (err instanceof DOMException && err.name === 'AbortError') return;
        setError(toApiError(err).toInfo());
      },
    );
    return () => ctl.abort();
  }, [id, nonce]);

  // Pending requests complete later: reload them when their session changes.
  useEffect(
    () =>
      eventsChanged.on((sid) => {
        if (detail && detail.sessionId === sid && detail.state === 'pending') setNonce((n) => n + 1);
      }),
    [detail],
  );

  return { detail, error, reload: useCallback(() => setNonce((n) => n + 1), []) };
}

export interface DetailsViewProps {
  d: EventDetail;
  tab: DetailTab;
  onTab: (t: DetailTab) => void;
}

/** DetailsView renders a loaded event (separate from loading, for component tests). */
export function DetailsView({ d, tab, onTab }: DetailsViewProps) {
  const tabs = tabsFor(d);
  const active = tabs.includes(tab) ? tab : tabs[0];
  const url = d.kind === 'dns' ? `${d.method ?? ''} ${d.host ?? ''}` : d.url || summaryUrl(d);
  return (
    <div className="details">
      <div className="details-head">
        <span className={`method-badge kind-${d.kind}`}>{d.kind === 'dns' ? 'DNS' : d.method || d.protocol || d.kind.toUpperCase()}</span>
        <span className="details-url mono" title={url}>
          {url}
        </span>
        {d.status ? <span className={`status-badge${d.status >= 400 ? ' error' : ''}`}>{d.status}</span> : null}
        {d.initiator === 'replay' && (
          <span className="badge replay" title="This request was replayed">
            <Icon name="replay" size={11} /> replay
          </span>
        )}
        <span className="spacer" />
        {d.kind === 'http' && (
          <button
            className="btn small"
            disabled={!d.replayable}
            title={d.replayable ? 'Send this request again' : d.encrypted ? 'Encrypted requests cannot be replayed' : 'This request cannot be replayed (not HTTP, or its body was truncated)'}
            onClick={() => void rowActions.replay(d.id)}
          >
            <Icon name="replay" /> Replay
          </button>
        )}
        <button className="icon-btn" aria-label="Close details" title="Close details" onClick={() => actions.selectEvent(null)}>
          <Icon name="close" />
        </button>
      </div>
      <Tabs tabs={tabs.map((t) => ({ id: t, label: TAB_LABELS[t] }))} active={active} onChange={(t) => onTab(t as DetailTab)} idPrefix="details" />
      <div className="details-body" role="tabpanel" id="details-panel" aria-labelledby={`details-tab-${active}`} tabIndex={0}>
        {d.encrypted && active === 'overview' && <EncryptedNotice d={d} />}
        {active === 'overview' && <OverviewTab d={d} />}
        {active === 'headers' && <HeadersTab d={d} />}
        {active === 'query' && <QueryTab d={d} />}
        {active === 'request' && <BodyViewer key={`${d.id}-req`} eventId={d.id} part="request" bodyRef={d.requestBody} kindHint={d.requestKind} />}
        {active === 'response' && <BodyViewer key={`${d.id}-resp`} eventId={d.id} part="response" bodyRef={d.responseBody} kindHint={d.responseKind} />}
        {active === 'timing' && <TimingTab d={d} />}
        {active === 'connection' && (
          <>
            {d.encrypted && <EncryptedNotice d={d} />}
            <ConnectionTab d={d} />
          </>
        )}
        {active === 'dns' && <DnsTab d={d} />}
        {active === 'messages' && <MessagesTab key={d.id} d={d} />}
      </div>
    </div>
  );
}

export function DetailsPanel({ ref }: { ref?: React.Ref<HTMLElement> }) {
  const id = useStore(appStore, (s) => s.selectedEventId);
  const { detail, error, reload } = useEventDetail(id);
  const [tab, setTab] = useState<DetailTab>(() => loadJSON<DetailTab>(TAB_KEY, 'overview'));
  const onTab = useCallback((t: DetailTab) => {
    setTab(t);
    saveJSON(TAB_KEY, t);
  }, []);

  let content: React.ReactNode;
  if (!id) content = <div className="empty-state">Select a request to see its details.</div>;
  else if (error)
    content = (
      <div className="pad">
        <ErrorView error={error} />
        <button className="btn" onClick={reload}>
          Retry
        </button>
      </div>
    );
  else if (!detail || detail.id !== id) content = <div className="loading">Loading…</div>;
  else content = <DetailsView d={detail} tab={tab} onTab={onTab} />;

  return (
    <section className="details-panel" aria-label="Request details" ref={ref} tabIndex={-1}>
      {content}
    </section>
  );
}
