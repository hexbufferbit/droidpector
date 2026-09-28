import { useCallback, useEffect, useState } from 'react';
import { api, toApiError } from '../../api/client';
import type { ErrorInfo, EventDetail } from '../../api/types';
import { ErrorView } from '../../components/ErrorView';
import { Icon, Illustration } from '../../components/Icon';
import { Tabs } from '../../components/Tabs';
import { copyText } from '../../lib/clipboard';
import { loadJSON, saveJSON } from '../../lib/storage';
import { summaryUrl } from '../../lib/url';
import { actions, appStore, eventsChanged, toast } from '../../state/app';
import { useStore } from '../../state/store';
import { methodLabel, methodTone, statusTone } from '../network/columns';
import { rowActions } from '../network/rowActions';
import { BodyViewer } from './body/BodyViewer';
import { EncryptedNotice } from './EncryptedNotice';
import { HeadersTab } from './HeadersTab';
import { ConnectionTab, DnsTab, QueryTab, TimingTab } from './InfoTabs';
import { MessagesTab } from './MessagesTab';
import { OverviewTab } from './OverviewTab';
import { isRawStream, TAB_LABELS, tabsFor, type DetailTab } from './tabsFor';

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

  // Pending requests complete (and long-lived streams grow) later: reload them when their session changes.
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
  const raw = isRawStream(d);
  const copyUrl = () => void copyText(url).then(() => toast('Copied URL'));
  return (
    <div className="details">
      <div className="details-head">
        <span className={`method-badge large tone-${methodTone(d)}`}>{methodLabel(d)}</span>
        <button className="details-url mono" title="Click to copy" onClick={copyUrl}>
          <span className="details-url-text">{url}</span>
          <Icon name="copy" size={11} className="details-url-copy" />
        </button>
        {d.status ? (
          <span className={`status-badge tone-${statusTone(d)}`}>
            {d.status}
            {d.statusText ? <span className="status-badge-text"> {d.statusText}</span> : null}
          </span>
        ) : d.state === 'pending' ? (
          <span className="status-badge tone-pending">
            <span className="spinner tiny" aria-hidden="true" /> pending
          </span>
        ) : null}
        {d.encrypted && (
          <span className="badge encrypted" title="HTTPS encrypted traffic: payload inspection unavailable">
            <Icon name="lock" size={10} /> encrypted
          </span>
        )}
        {raw && d.protocol && <span className="type-chip">{d.protocol}</span>}
        {d.initiator === 'replay' && (
          <span className="badge replay" title="This request was replayed">
            <Icon name="replay" size={11} /> replay
          </span>
        )}
        <span className="spacer" />
        {d.kind === 'http' && (
          <button
            className="btn small ghost"
            disabled={!d.replayable}
            title={d.replayable ? 'Send this request again' : d.encrypted ? 'Encrypted requests cannot be replayed' : 'This request cannot be replayed (not HTTP, or its body was truncated)'}
            onClick={() => void rowActions.replay(d.id)}
          >
            <Icon name="replay" /> Replay
          </button>
        )}
        <button className="icon-btn" aria-label="Close details" title="Close details (Esc)" onClick={() => actions.selectEvent(null)}>
          <Icon name="close" />
        </button>
      </div>
      <Tabs tabs={tabs.map((t) => ({ id: t, label: TAB_LABELS[t] }))} active={active} onChange={(t) => onTab(t as DetailTab)} idPrefix="details" />
      <div className="details-body" role="tabpanel" id="details-panel" aria-labelledby={`details-tab-${active}`} tabIndex={0}>
        {d.encrypted && active === 'overview' && <EncryptedNotice d={d} />}
        {active === 'overview' && <OverviewTab d={d} />}
        {active === 'headers' && <HeadersTab d={d} />}
        {active === 'query' && <QueryTab d={d} />}
        {active === 'request' && <BodyViewer key={`${d.id}-req`} eventId={d.id} part="request" bodyRef={d.requestBody} kindHint={d.requestKind} rawStream={raw} />}
        {active === 'response' && <BodyViewer key={`${d.id}-resp`} eventId={d.id} part="response" bodyRef={d.responseBody} kindHint={d.responseKind} rawStream={raw} />}
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
  if (!id)
    content = (
      <div className="empty-state details-empty">
        <Illustration name="cursor" size={48} />
        <p className="empty-title">Select a request</p>
        <p className="empty-sub">Headers, bodies, timing and connection details show up here. Double-click a row or press Enter to jump in.</p>
      </div>
    );
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
