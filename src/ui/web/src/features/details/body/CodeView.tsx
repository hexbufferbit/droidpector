import { useMemo } from 'react';

/** Above this many lines the gutter is skipped to keep huge bodies cheap to render. */
export const LINE_NUMBER_LIMIT = 20_000;

export function splitLines(text: string): string[] {
  const lines = text.split('\n');
  if (lines.length > 1 && lines[lines.length - 1] === '') lines.pop();
  return lines;
}

/** CodeView renders text in monospace with a line-number gutter (selection copies the code only). */
export function CodeView({ text, wrap = false, label }: { text: string; wrap?: boolean; label?: string }) {
  const lines = useMemo(() => splitLines(text), [text]);
  if (lines.length > LINE_NUMBER_LIMIT) {
    return (
      <pre className={`code-view mono${wrap ? ' wrap' : ''}`} aria-label={label}>
        {text}
      </pre>
    );
  }
  const width = String(lines.length).length;
  return (
    <div className={`code-lines mono${wrap ? ' wrap' : ''}`} aria-label={label} style={{ ['--gutter' as string]: `${Math.max(2, width)}ch` }}>
      {lines.map((l, i) => (
        <div className="code-line" key={i}>
          <span className="code-ln" aria-hidden="true">
            {i + 1}
          </span>
          <span className="code-text">{l || ' '}</span>
        </div>
      ))}
    </div>
  );
}
