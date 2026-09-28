import { useEffect, useMemo, useState } from 'react';
import { api, links, toApiError } from '../../../api/client';
import type { Body, BodyKind, BodyRef, ErrorInfo } from '../../../api/types';
import { ErrorView } from '../../../components/ErrorView';
import { Icon, Illustration } from '../../../components/Icon';
import { decodeUtf8 } from '../../../lib/bytes';
import { formatBytes, formatBytesExact } from '../../../lib/format';
import type { Json } from '../../../lib/jsonTree';
import { CodeView } from './CodeView';
import { HexView } from './HexView';
import { ImageView } from './ImageView';
import { JsonView } from './JsonView';
import { MarkupView } from './MarkupView';

/** Bodies above this size open in plain raw text to stay responsive. */
export const LARGE_BODY = 2 * 1024 * 1024;
const TEXT_RENDER_LIMIT = 8 * 1024 * 1024;

type Mode = 'tree' | 'pretty' | 'raw' | 'text' | 'hex' | 'preview';

export function modesFor(kind: BodyKind): Mode[] {
  switch (kind) {
    case 'json':
      return ['tree', 'raw', 'hex'];
    case 'xml':
    case 'html':
      return ['pretty', 'raw', 'hex'];
    case 'text':
      return ['text', 'hex'];
    case 'image':
      return ['preview', 'hex'];
    case 'binary':
      return ['hex'];
    default:
      return [];
  }
}

export function defaultMode(kind: BodyKind, size: number): Mode {
  if (size > LARGE_BODY && (kind === 'json' || kind === 'xml' || kind === 'html')) return 'raw';
  return modesFor(kind)[0] ?? 'text';
}

const MODE_LABEL: Record<Mode, string> = { tree: 'Tree', pretty: 'Pretty', raw: 'Raw', text: 'Text', hex: 'Hex', preview: 'Preview' };

function prettyJson(text: string): { value: Json | undefined; pretty: string } {
  try {
    const value = JSON.parse(text) as Json;
    return { value, pretty: JSON.stringify(value, null, 2) };
  } catch {
    return { value: undefined, pretty: text };
  }
}

export interface BodyViewerProps {
  eventId: string;
  part: 'request' | 'response';
  bodyRef?: BodyRef;
  /** kind classified by the core in the event detail */
  kindHint?: BodyKind;
  /** the body is the captured prefix of a raw TCP/TLS stream (shown as hex with a note) */
  rawStream?: boolean;
  /** injectable loader for tests */
  load?: (id: string, part: 'request' | 'response') => Promise<Body>;
}

export function BodyViewer({ eventId, part, bodyRef, kindHint, rawStream = false, load = api.body }: BodyViewerProps) {
  const [body, setBody] = useState<Body | null>(null);
  const [error, setError] = useState<ErrorInfo | null>(null);
  const [mode, setMode] = useState<Mode | null>(null);
  const [wrap, setWrap] = useState(true);

  useEffect(() => {
    let cancelled = false;
    setBody(null);
    setError(null);
    setMode(null);
    if (!bodyRef) return;
    load(eventId, part).then(
      (b) => !cancelled && setBody(b),
      (err) => !cancelled && setError(toApiError(err).toInfo()),
    );
    return () => {
      cancelled = true;
    };
  }, [eventId, part, bodyRef, load]);

  const kind: BodyKind = body ? (body.kind === 'empty' ? 'empty' : rawStream ? 'binary' : body.kind || kindHint || 'binary') : (kindHint ?? 'empty');
  const text = useMemo(() => (body && kind !== 'image' && kind !== 'binary' ? decodeUtf8(body.bytes) : ''), [body, kind]);
  const json = useMemo(() => (kind === 'json' && body && body.bytes.length <= LARGE_BODY * 4 ? prettyJson(text) : null), [kind, body, text]);

  if (!bodyRef) return <Empty rawStream={rawStream} part={part} />;
  if (error) return <ErrorView error={error} compact />;
  if (!body) return <div className="loading">Loading body…</div>;
  if (body.bytes.length === 0) return <Empty rawStream={rawStream} part={part} />;

  const modes = modesFor(kind);
  const active: Mode = mode && modes.includes(mode) ? mode : defaultMode(kind, body.bytes.length);
  const download = links.bodyDownload(eventId, part);
  const truncated = body.truncated || !!bodyRef.truncated;
  const stored = bodyRef.stored || body.bytes.length;
  const full = bodyRef.size || body.size;

  let view: React.ReactNode;
  switch (active) {
    case 'tree':
      view = json && json.value !== undefined ? <JsonView value={json.value} /> : <RawText text={text} wrap={wrap} note="The body is not valid JSON; showing it as text." />;
      break;
    case 'raw':
      view = <RawText text={kind === 'json' && json ? json.pretty : text} wrap={wrap} />;
      break;
    case 'pretty':
      view = <MarkupView text={text} pretty />;
      break;
    case 'text':
      view = <RawText text={text} wrap={wrap} />;
      break;
    case 'preview':
      view = <ImageView bytes={body.bytes} contentType={body.contentType || bodyRef.mime || ''} />;
      break;
    default:
      view = <HexView bytes={body.bytes} downloadHref={download} />;
  }

  return (
    <div className="body-viewer">
      {rawStream && (
        <div className="banner info small" role="note">
          <Icon name="info" /> Raw TCP stream ({part === 'request' ? 'sent by the app' : 'received from the server'}, first {formatBytesExact(body.bytes.length)}
          {full > body.bytes.length ? ` of ${formatBytesExact(full)}` : ''}).
        </div>
      )}
      {truncated && !rawStream && (
        <div className="banner warning small" role="note">
          <Icon name="warning" /> Body truncated: {formatBytesExact(stored)} of {formatBytesExact(full)} captured.
        </div>
      )}
      {body.decodeWarning && (
        <div className="banner warning small" role="note">
          <Icon name="warning" /> {body.decodeWarning}
        </div>
      )}
      <div className="viewer-toolbar">
        {modes.length > 1 && (
          <div className="segmented" role="group" aria-label="Body view">
            {modes.map((m) => (
              <button key={m} className={`seg${m === active ? ' active' : ''}`} aria-pressed={m === active} onClick={() => setMode(m)}>
                {MODE_LABEL[m]}
              </button>
            ))}
          </div>
        )}
        {(active === 'raw' || active === 'text') && (
          <label className="check">
            <input type="checkbox" checked={wrap} onChange={(e) => setWrap(e.target.checked)} /> Wrap lines
          </label>
        )}
        <span className="muted body-size num">
          {rawStream ? 'STREAM' : kind.toUpperCase()} · {formatBytes(body.bytes.length)}
        </span>
        <a className="btn small ghost" href={download} download title="Download the body">
          <Icon name="download" /> Download
        </a>
      </div>
      <div className="viewer-content">{view}</div>
    </div>
  );
}

function Empty({ rawStream, part }: { rawStream: boolean; part: 'request' | 'response' }) {
  return (
    <div className="empty-state">
      <Illustration name="inbox" size={44} />
      <p className="empty-title">No body</p>
      <p className="empty-sub">{rawStream ? `No ${part === 'request' ? 'outgoing' : 'incoming'} bytes were captured on this stream.` : `This ${part} carried no body.`}</p>
    </div>
  );
}

function RawText({ text, wrap, note }: { text: string; wrap: boolean; note?: string }) {
  const shown = text.length > TEXT_RENDER_LIMIT ? text.slice(0, TEXT_RENDER_LIMIT) : text;
  return (
    <>
      {note && <div className="banner info small">{note}</div>}
      {shown.length < text.length && <div className="banner info small">Showing the first {formatBytes(shown.length)}; download the body to see everything.</div>}
      <CodeView text={shown} wrap={wrap} />
    </>
  );
}
