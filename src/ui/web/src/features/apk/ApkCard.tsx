import type { UploadState } from '../../state/app';
import { actions } from '../../state/app';
import { ErrorView } from '../../components/ErrorView';
import { Icon } from '../../components/Icon';
import { formatBytes } from '../../lib/format';

/** ApkCard shows the analysis of an uploaded APK and the progress of the one-click install. */
export function ApkCard({ upload }: { upload: UploadState }) {
  const e = upload.entry;
  const info = e?.info;
  const problems = e?.problems ?? [];
  const blocking = !!e && (!e.valid || problems.some((p) => p.severity === 'error'));
  const pct = upload.size ? Math.round((upload.loaded / upload.size) * 100) : 0;
  return (
    <div className="apk-card" role="region" aria-label="APK">
      <div className="apk-card-head">
        <div className="apk-icon" aria-hidden="true">
          <Icon name="android" size={28} />
        </div>
        <div className="apk-title">
          <div className="apk-label">{info?.label || info?.package || upload.fileName}</div>
          <div className="muted mono small">{info?.package ?? upload.fileName}</div>
        </div>
        <button className="icon-btn" aria-label="Dismiss" title="Dismiss" onClick={actions.dismissUpload}>
          <Icon name="close" />
        </button>
      </div>

      {upload.phase === 'uploading' && (
        <div className="apk-progress">
          <div className="progress">
            <div className="progress-fill" style={{ width: `${pct}%` }} />
          </div>
          <span className="muted small">
            Uploading {upload.fileName} · {formatBytes(upload.loaded)} of {formatBytes(upload.size)}
          </span>
        </div>
      )}

      {info && (
        <dl className="kv compact apk-facts">
          <dt>Version</dt>
          <dd>
            {info.versionName || '—'} <span className="muted">({info.versionCode})</span>
          </dd>
          <dt>Min / target SDK</dt>
          <dd>
            {info.minSdkCodename || info.minSdk} / {info.targetSdk}
          </dd>
          <dt>ABIs</dt>
          <dd>{info.abis?.length ? info.abis.join(', ') : 'none (no native code)'}</dd>
          <dt>File</dt>
          <dd>
            {e?.fileName} · {formatBytes(e?.size ?? upload.size)}
          </dd>
        </dl>
      )}

      {e?.runtime?.reason && (
        <div className="apk-runtime">
          <span className="badge">{e.runtime.runtime}</span>
          {e.runtime.translated && <span className="badge translated">runs through ARM translation</span>}
          <p className="small">{e.runtime.reason}</p>
        </div>
      )}

      {problems.length > 0 && (
        <ul className="apk-problems" aria-label="Validation problems">
          {problems.map((p, i) => (
            <li key={i} className={`sev-${p.severity}`}>
              <Icon name={p.severity === 'error' ? 'error' : 'warning'} /> <span>{p.message}</span>
            </li>
          ))}
        </ul>
      )}

      {blocking && <p className="apk-blocked">This APK cannot be installed. Fix the problems above and add it again.</p>}
      {upload.phase === 'starting' && (
        <p className="apk-status">
          <span className="spinner" aria-hidden="true" /> Installing and launching… progress is shown in the status bar.
        </p>
      )}
      {upload.phase === 'failed' && upload.error && <ErrorView error={upload.error} compact />}
    </div>
  );
}
