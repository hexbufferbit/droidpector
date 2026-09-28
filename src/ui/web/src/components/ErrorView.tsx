import { useState } from 'react';
import type { ErrorInfo } from '../api/types';
import { copyText } from '../lib/clipboard';
import { Icon } from './Icon';

/** ErrorView shows an error's title, possible causes and collapsible details. */
export function ErrorView({ error, compact = false }: { error: ErrorInfo; compact?: boolean }) {
  const [open, setOpen] = useState(false);
  const [copied, setCopied] = useState(false);
  const causes = error.causes ?? [];
  return (
    <div className={`error-view${compact ? ' compact' : ''}`}>
      <p className="error-title">{error.title || 'The operation failed.'}</p>
      {causes.length > 0 && (
        <>
          <p className="error-causes-label">Possible causes:</p>
          <ul className="error-causes">
            {causes.map((c, i) => (
              <li key={i}>{c}</li>
            ))}
          </ul>
        </>
      )}
      {error.details && (
        <div className="error-details">
          <button className="link-btn" aria-expanded={open} onClick={() => setOpen(!open)}>
            <Icon name={open ? 'chevronDown' : 'chevronRight'} /> Details
          </button>
          {open && (
            <div className="error-details-body">
              <button
                className="btn small copy-details"
                onClick={() => {
                  void copyText(error.details ?? '').then(() => {
                    setCopied(true);
                    setTimeout(() => setCopied(false), 1500);
                  });
                }}
              >
                <Icon name="copy" /> {copied ? 'Copied' : 'Copy'}
              </button>
              <pre className="mono">{error.details}</pre>
            </div>
          )}
        </div>
      )}
      {error.code && <p className="error-code">Error code: {error.code}</p>}
    </div>
  );
}
