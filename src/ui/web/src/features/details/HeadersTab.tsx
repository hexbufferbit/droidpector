import { useState } from 'react';
import type { EventDetail, Header } from '../../api/types';
import { Icon } from '../../components/Icon';
import { copyText } from '../../lib/clipboard';
import { toast } from '../../state/app';
import { CodeView } from './body/CodeView';

export function sortHeaders(h: Header[] | undefined): Header[] {
  return [...(h ?? [])].sort((a, b) => a.name.toLowerCase().localeCompare(b.name.toLowerCase()));
}

export function headersText(h: Header[] | undefined, firstLine?: string): string {
  const lines = (h ?? []).map((x) => `${x.name}: ${x.value}`);
  return (firstLine ? [firstLine, ...lines] : lines).join('\n');
}

function Section({ title, headers, firstLine, raw }: { title: string; headers?: Header[]; firstLine: string; raw: boolean }) {
  const [open, setOpen] = useState(true);
  const list = sortHeaders(headers);
  return (
    <section className="headers-section" aria-label={title}>
      <div className="section-head">
        <button className="link-btn section-toggle" aria-expanded={open} onClick={() => setOpen(!open)}>
          <Icon name={open ? 'chevronDown' : 'chevronRight'} size={10} /> {title} <span className="count-pill">{list.length}</span>
        </button>
        {!raw && (
          <span className="section-first-line mono muted" title={firstLine}>
            {firstLine}
          </span>
        )}
        <button
          className="btn small ghost"
          onClick={() => void copyText(headersText(headers, firstLine)).then(() => toast(`Copied ${title.toLowerCase()}`))}
          title={`Copy ${title.toLowerCase()}`}
        >
          <Icon name="copy" /> Copy
        </button>
      </div>
      {open &&
        (list.length === 0 ? (
          <p className="muted pad">No headers</p>
        ) : raw ? (
          <CodeView text={headersText(headers, firstLine)} wrap />
        ) : (
          <table className="kv-table headers-table">
            <tbody>
              {list.map((h, i) => (
                <tr key={`${h.name}-${i}`}>
                  <th scope="row">{h.name}</th>
                  <td className="mono selectable">{h.value}</td>
                </tr>
              ))}
            </tbody>
          </table>
        ))}
    </section>
  );
}

export function HeadersTab({ d }: { d: EventDetail }) {
  const [raw, setRaw] = useState(false);
  const target = (d.path || '/') + (d.query ? `?${d.query}` : '');
  const proto = d.protocol || 'HTTP/1.1';
  return (
    <div className="headers-tab">
      <div className="viewer-toolbar">
        <label className="check">
          <input type="checkbox" checked={raw} onChange={(e) => setRaw(e.target.checked)} /> Raw
        </label>
      </div>
      <Section title="Request headers" headers={d.requestHeaders} firstLine={`${d.method ?? 'GET'} ${target} ${proto}`} raw={raw} />
      <Section title="Response headers" headers={d.responseHeaders} firstLine={`${proto} ${d.status ?? ''} ${d.statusText ?? ''}`.trim()} raw={raw} />
    </div>
  );
}
