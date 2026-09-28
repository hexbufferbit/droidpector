import type { EventDetail, Timing } from '../../api/types';
import { formatBytes, formatDateTime, formatDuration } from '../../lib/format';
import { reasonText } from './EncryptedNotice';

export function QueryTab({ d }: { d: EventDetail }) {
  const params = d.queryParams ?? [];
  if (params.length === 0) return <div className="empty-state">No query parameters</div>;
  return (
    <table className="kv-table query-table">
      <thead>
        <tr>
          <th scope="col">Name</th>
          <th scope="col">Value</th>
        </tr>
      </thead>
      <tbody>
        {params.map((p, i) => (
          <tr key={i}>
            <td className="mono selectable">{p.name}</td>
            <td className="mono selectable">{p.value}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

export const PHASES: { key: keyof Timing; label: string; cls: string }[] = [
  { key: 'blocked', label: 'Queueing / blocked', cls: 't-blocked' },
  { key: 'dns', label: 'DNS lookup', cls: 't-dns' },
  { key: 'connect', label: 'Initial connection', cls: 't-connect' },
  { key: 'tls', label: 'TLS handshake', cls: 't-tls' },
  { key: 'send', label: 'Request sent', cls: 't-send' },
  { key: 'wait', label: 'Waiting for server response', cls: 't-wait' },
  { key: 'receive', label: 'Content download', cls: 't-receive' },
];

export interface WaterfallBar {
  key: keyof Timing;
  label: string;
  cls: string;
  start: number;
  duration: number; // -1 = n/a
}

/** waterfall lays timing phases on a time axis (HAR semantics: TLS is part of connect). */
export function waterfall(t: Timing): { bars: WaterfallBar[]; total: number } {
  let cursor = 0;
  const bars: WaterfallBar[] = [];
  let connectStart = 0;
  for (const p of PHASES) {
    const v = t[p.key];
    const valid = typeof v === 'number' && v >= 0;
    if (p.key === 'tls') {
      const connect = t.connect >= 0 ? t.connect : 0;
      const start = valid ? connectStart + Math.max(0, connect - v) : cursor;
      bars.push({ ...p, start, duration: valid ? v : -1 });
      if (valid && t.connect < 0) cursor = start + v;
      continue;
    }
    if (p.key === 'connect') connectStart = cursor;
    bars.push({ ...p, start: cursor, duration: valid ? v : -1 });
    if (valid) cursor += v;
  }
  return { bars, total: cursor };
}

export function TimingTab({ d }: { d: EventDetail }) {
  if (!d.timing) return <div className="empty-state">No timing information</div>;
  const { bars, total } = waterfall(d.timing);
  const scale = total > 0 ? total : 1;
  return (
    <div className="timing-tab">
      <p className="muted">Started {formatDateTime(d.timestamp)}</p>
      <table className="timing-table">
        <tbody>
          {bars.map((b) => (
            <tr key={b.key}>
              <th scope="row">{b.label}</th>
              <td className="timing-bar-cell">
                {b.duration >= 0 && (
                  <div
                    className={`timing-bar ${b.cls}`}
                    style={{ left: `${(b.start / scale) * 100}%`, width: `max(2px, ${(b.duration / scale) * 100}%)` }}
                    title={`${b.label}: ${formatDuration(b.duration)}`}
                  />
                )}
              </td>
              <td className="timing-value">{b.duration >= 0 ? formatDuration(b.duration) : <span className="muted">n/a</span>}</td>
            </tr>
          ))}
          <tr className="timing-total">
            <th scope="row">Total</th>
            <td />
            <td className="timing-value">{formatDuration(d.durationMs >= 0 ? d.durationMs : total)}</td>
          </tr>
        </tbody>
      </table>
    </div>
  );
}

export function ConnectionTab({ d }: { d: EventDetail }) {
  const c = d.conn;
  const t = d.tls;
  return (
    <div className="connection-tab">
      <h4>Connection</h4>
      {c ? (
        <dl className="kv">
          <dt>Client (app)</dt>
          <dd className="mono">{c.clientAddr || '—'}</dd>
          <dt>Server (destination)</dt>
          <dd className="mono">{c.serverAddr || '—'}</dd>
          <dt>Remote address</dt>
          <dd className="mono">{c.remoteAddr || '—'}</dd>
          {c.reused && (
            <>
              <dt>Reused</dt>
              <dd>Yes</dd>
            </>
          )}
          {(c.bytesUp > 0 || c.bytesDown > 0) && (
            <>
              <dt>Bytes sent / received</dt>
              <dd>
                {formatBytes(c.bytesUp)} / {formatBytes(c.bytesDown)}
              </dd>
            </>
          )}
        </dl>
      ) : (
        <p className="muted">No connection information.</p>
      )}
      {t && (
        <>
          <h4>TLS</h4>
          <dl className="kv">
            <dt>Version</dt>
            <dd>{t.version || '—'}</dd>
            <dt>Cipher suite</dt>
            <dd className="mono">{t.cipherSuite || '—'}</dd>
            <dt>ALPN</dt>
            <dd>
              {t.alpn || '—'}
              {t.clientAlpn?.length ? <span className="muted"> (offered: {t.clientAlpn.join(', ')})</span> : null}
            </dd>
            <dt>SNI</dt>
            <dd>{t.sni || '—'}</dd>
            <dt>Intercepted</dt>
            <dd>{t.intercepted ? 'Yes — decrypted by the sandbox' : 'No — passed through encrypted'}</dd>
            {t.passthroughReason && (
              <>
                <dt>Passthrough reason</dt>
                <dd>{reasonText(t.passthroughReason)}</dd>
              </>
            )}
          </dl>
          {t.serverCerts && t.serverCerts.length > 0 && (
            <>
              <h4>Server certificate chain</h4>
              <ol className="cert-chain">
                {t.serverCerts.map((cert, i) => {
                  const now = Date.now();
                  const expired = new Date(cert.notAfter).getTime() < now || new Date(cert.notBefore).getTime() > now;
                  return (
                    <li key={i} className="cert">
                      <dl className="kv compact">
                        <dt>Subject</dt>
                        <dd className="mono">{cert.subject}</dd>
                        <dt>Issuer</dt>
                        <dd className="mono">{cert.issuer}</dd>
                        <dt>Valid</dt>
                        <dd className={expired ? 'text-error' : ''}>
                          {formatDateTime(cert.notBefore)} → {formatDateTime(cert.notAfter)}
                          {expired ? ' (not currently valid)' : ''}
                        </dd>
                        {cert.dnsNames?.length ? (
                          <>
                            <dt>DNS names</dt>
                            <dd>{cert.dnsNames.join(', ')}</dd>
                          </>
                        ) : null}
                        <dt>SHA-256</dt>
                        <dd className="mono small selectable">{cert.sha256}</dd>
                      </dl>
                    </li>
                  );
                })}
              </ol>
            </>
          )}
        </>
      )}
    </div>
  );
}

export function DnsTab({ d }: { d: EventDetail }) {
  const dns = d.dns;
  if (!dns) return <div className="empty-state">No DNS information</div>;
  return (
    <div className="dns-tab">
      <dl className="kv">
        <dt>Question</dt>
        <dd className="mono">{dns.question}</dd>
        <dt>Type</dt>
        <dd>{dns.qtype}</dd>
        <dt>Response code</dt>
        <dd className={dns.rcode && dns.rcode !== 'NOERROR' ? 'text-error' : ''}>{dns.rcode || '—'}</dd>
      </dl>
      <h4>Answers</h4>
      {dns.answers?.length ? (
        <table className="kv-table">
          <thead>
            <tr>
              <th scope="col">Name</th>
              <th scope="col">Type</th>
              <th scope="col">TTL</th>
              <th scope="col">Data</th>
            </tr>
          </thead>
          <tbody>
            {dns.answers.map((a, i) => (
              <tr key={i}>
                <td className="mono">{a.name}</td>
                <td>{a.type}</td>
                <td>{a.ttl}s</td>
                <td className="mono selectable">{a.data}</td>
              </tr>
            ))}
          </tbody>
        </table>
      ) : (
        <p className="muted">No answers</p>
      )}
    </div>
  );
}
