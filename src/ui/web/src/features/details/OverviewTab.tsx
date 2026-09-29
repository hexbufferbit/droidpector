import type { EventDetail } from '../../api/types';
import { formatBytes, formatBytesExact, formatDateTime, formatDuration } from '../../lib/format';
import { actions } from '../../state/app';
import { summaryUrl } from '../../lib/url';
import { isRawStream } from './tabsFor';

function Row({ label, children }: { label: string; children: React.ReactNode }) {
  if (children === undefined || children === null || children === '') return null;
  return (
    <>
      <dt>{label}</dt>
      <dd>{children}</dd>
    </>
  );
}

export function OverviewTab({ d }: { d: EventDetail }) {
  const url = d.kind === 'dns' ? '' : d.url || summaryUrl(d);
  const end = d.timestamp && d.durationMs >= 0 ? new Date(new Date(d.timestamp).getTime() + d.durationMs) : null;
  const pending = d.state === 'pending';
  const raw = isRawStream(d);
  return (
    <dl className="kv overview">
      <Row label="URL">{url ? <span className="mono selectable url">{url}</span> : null}</Row>
      {d.kind === 'dns' && <Row label="Query">{`${d.method ?? ''} ${d.host ?? ''}`}</Row>}
      <Row label="Method">{d.kind === 'dns' ? null : d.method}</Row>
      <Row label="Status">
        {d.status ? (
          <span className={d.status >= 400 ? 'text-error' : ''}>
            {d.status} {d.statusText}
          </span>
        ) : pending ? (
          <span className="live-text">
            <span className="spinner tiny" aria-hidden="true" /> {raw ? 'Open (streaming)' : 'Pending'}
          </span>
        ) : null}
      </Row>
      <Row label="Error">{d.error ? <span className="text-error">{d.error}</span> : null}</Row>
      <Row label="Protocol">{d.protocol}</Row>
      <Row label="Kind">{`${d.kind} · ${d.category}`}</Row>
      <Row label="Content type">{d.mime}</Row>
      <Row label="Remote address">{d.conn?.remoteAddr || d.conn?.serverAddr}</Row>
      <Row label="Host interface">{d.conn?.interface}</Row>
      <Row label="Initiator">
        {d.initiator === 'replay' ? (
          <>
            Replay
            {d.replayOf && (
              <>
                {' of '}
                <button className="link-btn" onClick={() => actions.selectEvent(d.replayOf ?? null)}>
                  original request
                </button>
              </>
            )}
          </>
        ) : (
          'App (guest)'
        )}
      </Row>
      <Row label="Started">{formatDateTime(d.timestamp)}</Row>
      <Row label="Finished">{end && !pending ? formatDateTime(end) : null}</Row>
      <Row label="Duration">{pending ? (d.durationMs > 0 ? `${formatDuration(d.durationMs)} so far` : 'pending') : formatDuration(d.durationMs)}</Row>
      <Row label={raw ? 'Bytes sent' : 'Request size'}>{d.kind === 'dns' ? null : `${formatBytes(d.requestSize)} (${formatBytesExact(d.requestSize)})`}</Row>
      <Row label={raw ? 'Bytes received' : 'Response size'}>{d.kind === 'dns' ? null : `${formatBytes(d.responseSize)} (${formatBytesExact(d.responseSize)})`}</Row>
      <Row label="Package">{d.package}</Row>
      <Row label="Encrypted">{d.encrypted ? 'Yes — payload not inspectable' : null}</Row>
    </dl>
  );
}
