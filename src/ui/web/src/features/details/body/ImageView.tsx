import { useEffect, useState } from 'react';
import { formatBytes } from '../../../lib/format';

export function ImageView({ bytes, contentType }: { bytes: Uint8Array; contentType: string }) {
  const [url, setUrl] = useState<string | null>(null);
  const [dims, setDims] = useState<{ w: number; h: number } | null>(null);
  const [failed, setFailed] = useState(false);
  useEffect(() => {
    const type = contentType.startsWith('image/') ? contentType.split(';')[0] : 'image/*';
    const u = URL.createObjectURL(new Blob([bytes as BlobPart], { type }));
    setUrl(u);
    setFailed(false);
    setDims(null);
    return () => URL.revokeObjectURL(u);
  }, [bytes, contentType]);
  if (!url) return null;
  return (
    <div className="image-view">
      {failed ? (
        <p className="muted">The image could not be decoded.</p>
      ) : (
        <div className="image-frame">
          <img
            src={url}
            alt="Response image preview"
            onLoad={(e) => setDims({ w: e.currentTarget.naturalWidth, h: e.currentTarget.naturalHeight })}
            onError={() => setFailed(true)}
          />
        </div>
      )}
      <p className="muted image-meta">
        {dims ? `${dims.w} × ${dims.h} px · ` : ''}
        {formatBytes(bytes.length)} · {contentType || 'image'}
      </p>
    </div>
  );
}
