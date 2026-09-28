import { useMemo } from 'react';
import { formatMarkup, tokenizeMarkup } from '../../../lib/markup';

const HIGHLIGHT_LIMIT = 512 * 1024;

/** MarkupView shows XML/HTML as highlighted source — never rendered as a live page. */
export function MarkupView({ text, pretty }: { text: string; pretty: boolean }) {
  const src = useMemo(() => (pretty && text.length <= HIGHLIGHT_LIMIT ? formatMarkup(text) : text), [text, pretty]);
  const tokens = useMemo(() => (src.length <= HIGHLIGHT_LIMIT ? tokenizeMarkup(src) : null), [src]);
  return (
    <pre className="code-view mono wrap">
      {tokens ? tokens.map((t, i) => (t.type === 'text' ? t.text : <span key={i} className={`m-${t.type}`}>{t.text}</span>)) : src}
    </pre>
  );
}
