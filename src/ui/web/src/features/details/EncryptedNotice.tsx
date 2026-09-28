import type { EventDetail } from '../../api/types';
import { Icon } from '../../components/Icon';
import { formatBytes } from '../../lib/format';

export const ENCRYPTED_MESSAGE = 'HTTPS encrypted traffic detected. Payload inspection unavailable for this connection.';

/** reasonText removes the generic sentence the core prefixes to passthrough reasons. */
export function reasonText(reason: string | undefined): string {
  if (!reason) return '';
  const r = reason.trim();
  const prefix = ENCRYPTED_MESSAGE.replace(/\.$/, '');
  if (r.startsWith(prefix)) return r.slice(prefix.length).replace(/^[\s.:;,-]+/, '').replace(/^./, (c) => c.toUpperCase());
  return r;
}

export function EncryptedNotice({ d }: { d: EventDetail }) {
  const reason = reasonText(d.tls?.passthroughReason);
  const up = d.conn?.bytesUp || d.requestSize;
  const down = d.conn?.bytesDown || d.responseSize;
  return (
    <div className="encrypted-notice" role="note">
      <div className="encrypted-title">
        <Icon name="lock" size={16} />
        <strong>{ENCRYPTED_MESSAGE}</strong>
      </div>
      {reason && (
        <p>
          <span className="muted">Reason: </span>
          {reason}
        </p>
      )}
      <dl className="kv compact">
        {d.tls?.sni && (
          <>
            <dt>Server name (SNI)</dt>
            <dd>{d.tls.sni}</dd>
          </>
        )}
        {d.host && (
          <>
            <dt>Host</dt>
            <dd>
              {d.host}
              {d.port ? `:${d.port}` : ''}
            </dd>
          </>
        )}
        {d.tls?.version && (
          <>
            <dt>TLS</dt>
            <dd>
              {d.tls.version}
              {d.tls.alpn ? ` · ALPN ${d.tls.alpn}` : ''}
            </dd>
          </>
        )}
        <dt>Data sent / received</dt>
        <dd>
          {formatBytes(up)} / {formatBytes(down)}
        </dd>
      </dl>
    </div>
  );
}
