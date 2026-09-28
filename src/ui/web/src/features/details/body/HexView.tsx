import { useMemo } from 'react';
import { hexdump, HEXDUMP_LIMIT } from '../../../lib/hexdump';
import { formatBytes } from '../../../lib/format';

export function HexView({ bytes, downloadHref }: { bytes: Uint8Array; downloadHref?: string }) {
  const dump = useMemo(() => hexdump(bytes, HEXDUMP_LIMIT), [bytes]);
  return (
    <div className="hex-view">
      {dump.truncated && (
        <div className="banner info small">
          Showing the first {formatBytes(dump.shown)} of {formatBytes(bytes.length)}.{' '}
          {downloadHref && (
            <a href={downloadHref} download>
              Download the full body
            </a>
          )}
        </div>
      )}
      <pre className="code-view mono" aria-label="Hex dump">
        {dump.text}
      </pre>
    </div>
  );
}
