import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { api, toApiError } from '../../api/client';
import type { ErrorInfo, EventDetail, WSFrame } from '../../api/types';
import { ErrorView } from '../../components/ErrorView';
import { Icon } from '../../components/Icon';
import { base64ToBytes, decodeUtf8 } from '../../lib/bytes';
import { formatBytes, formatClock } from '../../lib/format';
import type { Json } from '../../lib/jsonTree';
import { eventsChanged } from '../../state/app';
import { HexView } from './body/HexView';
import { JsonView } from './body/JsonView';

const PAGE = 1000;

export const OPCODES: Record<number, string> = { 0: 'continuation', 1: 'text', 2: 'binary', 8: 'close', 9: 'ping', 10: 'pong' };

export function framePreview(f: WSFrame, max = 120): string {
  const bytes = base64ToBytes(f.data);
  if (f.opcode === 8 && bytes.length >= 2) {
    const code = (bytes[0] << 8) | bytes[1];
    const reason = decodeUtf8(bytes.subarray(2));
    return `close ${code}${reason ? ` ${reason}` : ''}`;
  }
  if (f.opcode === 2) return `binary, ${formatBytes(f.length)}`;
  const t = decodeUtf8(bytes.subarray(0, max * 4));
  return t.length > max ? t.slice(0, max) + '…' : t;
}

type FrameMode = 'text' | 'json' | 'hex';

function FrameDetail({ frame }: { frame: WSFrame }) {
  const bytes = useMemo(() => base64ToBytes(frame.data), [frame]);
  const text = useMemo(() => decodeUtf8(bytes), [bytes]);
  const json = useMemo(() => {
    try {
      return { v: JSON.parse(text) as Json };
    } catch {
      return null;
    }
  }, [text]);
  const [mode, setMode] = useState<FrameMode>(json ? 'json' : frame.opcode === 2 ? 'hex' : 'text');
  const modes: FrameMode[] = json ? ['json', 'text', 'hex'] : ['text', 'hex'];
  return (
    <div className="frame-detail">
      <div className="viewer-toolbar">
        <div className="segmented" role="group" aria-label="Message view">
          {modes.map((m) => (
            <button key={m} className={m === mode ? 'active' : ''} aria-pressed={m === mode} onClick={() => setMode(m)}>
              {m === 'json' ? 'JSON' : m === 'hex' ? 'Hex' : 'Text'}
            </button>
          ))}
        </div>
        <span className="muted">
          {frame.outgoing ? 'Sent' : 'Received'} · {OPCODES[frame.opcode] ?? `opcode ${frame.opcode}`} · {formatBytes(frame.length)}
          {frame.truncated ? ' · truncated' : ''}
        </span>
      </div>
      {mode === 'json' && json ? <JsonView value={json.v} /> : mode === 'hex' ? <HexView bytes={bytes} /> : <pre className="code-view mono wrap">{text}</pre>}
    </div>
  );
}

export function MessagesTab({ d, loadFrames = api.frames }: { d: EventDetail; loadFrames?: (id: string, from?: number) => Promise<WSFrame[]> }) {
  const [frames, setFrames] = useState<WSFrame[] | null>(null);
  const [error, setError] = useState<ErrorInfo | null>(null);
  const [selected, setSelected] = useState<number | null>(null);
  const [more, setMore] = useState(false);

  const load = useCallback(
    async (from: number, append: boolean) => {
      try {
        const page = (await loadFrames(d.id, from)) ?? [];
        setFrames((prev) => (append && prev ? [...prev, ...page.filter((f) => f.seq > (prev[prev.length - 1]?.seq ?? -1))] : page));
        setMore(page.length >= PAGE);
        setError(null);
      } catch (err) {
        setError(toApiError(err).toInfo());
      }
    },
    [d.id, loadFrames],
  );

  useEffect(() => {
    setFrames(null);
    setSelected(null);
    void load(0, false);
  }, [load]);

  // Live connections keep receiving frames.
  const framesRef = useRef<WSFrame[] | null>(null);
  framesRef.current = frames;
  useEffect(
    () =>
      eventsChanged.on((sid) => {
        if (sid !== d.sessionId || !framesRef.current) return;
        const last = framesRef.current[framesRef.current.length - 1]?.seq ?? -1;
        void load(last + 1, true);
      }),
    [d.sessionId, load],
  );

  if (error) return <ErrorView error={error} compact />;
  if (!frames) return <div className="loading">Loading messages…</div>;
  if (frames.length === 0) return <div className="empty-state">No messages</div>;
  const sel = frames.find((f) => f.seq === selected) ?? null;
  return (
    <div className="messages-tab">
      <div className="frames-list" role="listbox" aria-label="WebSocket messages">
        {frames.map((f) => (
          <div
            key={f.seq}
            role="option"
            aria-selected={f.seq === selected}
            tabIndex={0}
            className={`frame-row${f.outgoing ? ' out' : ' in'}${f.seq === selected ? ' selected' : ''}${f.opcode >= 8 ? ' control' : ''}`}
            onClick={() => setSelected(f.seq)}
            onKeyDown={(e) => (e.key === 'Enter' || e.key === ' ') && setSelected(f.seq)}
          >
            <span className="frame-dir" title={f.outgoing ? 'Sent by the app' : 'Received from the server'} aria-label={f.outgoing ? 'Sent' : 'Received'}>
              <Icon name={f.outgoing ? 'up' : 'down'} size={11} />
            </span>
            <span className="frame-time">{formatClock(f.time)}</span>
            <span className="frame-op">{OPCODES[f.opcode] ?? f.opcode}</span>
            <span className="frame-len">{formatBytes(f.length)}</span>
            <span className="frame-data mono">{framePreview(f)}</span>
          </div>
        ))}
        {more && (
          <button className="btn small load-more" onClick={() => void load((frames[frames.length - 1]?.seq ?? 0) + 1, true)}>
            Load more messages
          </button>
        )}
      </div>
      {sel ? <FrameDetail key={sel.seq} frame={sel} /> : <p className="muted pad">Select a message to see its full data.</p>}
    </div>
  );
}
