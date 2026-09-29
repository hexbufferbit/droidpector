import { useEffect, useState } from 'react';
import type { UploadState } from '../../state/app';
import { actions, appStore } from '../../state/app';
import { useStore } from '../../state/store';
import { ErrorView } from '../../components/ErrorView';
import { Icon } from '../../components/Icon';
import { formatBytes } from '../../lib/format';
import { STEPS, expectedBootMs, installProgress } from '../../lib/installProgress';

/** InstallStatus is the live progress of the one-click install (it runs by itself; the card only reports it). */
function InstallStatus({ pkg }: { pkg?: string }) {
  const status = useStore(appStore, (s) => s.status);
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const t = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(t);
  }, []);
  const p = installProgress(status, pkg, now, expectedBootMs(!!status?.accelerated));
  return (
    <div className="install-status" role="status" aria-live="polite">
      <div className="install-status-row">
        <span className="spinner" aria-hidden="true" />
        <span className="install-label">
          Step {p.step} of {STEPS.length}: {p.label}
        </span>
        <span className="install-pct num">{p.pct}%</span>
      </div>
      <div className="progress" role="progressbar" aria-valuemin={0} aria-valuemax={100} aria-valuenow={p.pct} aria-label="Install progress">
        <div className="progress-fill" style={{ width: `${p.pct}%` }} />
      </div>
      <ol className="install-steps">
        {STEPS.map((name, i) => (
          <li key={name} className={i + 1 < p.step ? 'done' : i + 1 === p.step ? 'active' : ''}>
            {name}
          </li>
        ))}
      </ol>
    </div>
  );
}

/** ApkCard shows the analysis of an uploaded APK and the progress of the one-click install. */
export function ApkCard({ upload }: { upload: UploadState }) {
  const e = upload.entry;
  const info = e?.info;
  const problems = e?.problems ?? [];
  const blocking = !!e && (!e.valid || problems.some((p) => p.severity === 'error'));
  const pct = upload.size ? Math.round((upload.loaded / upload.size) * 100) : 0;
  const analysis = (
    <>
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
    </>
  );
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

      {upload.phase === 'starting' ? (
        // Installing: the card shrinks to its progress so the phone stays
        // visible; the analysis stays one click away.
        <>
          <InstallStatus pkg={info?.package} />
          <details className="apk-more">
            <summary>APK details</summary>
            {analysis}
          </details>
        </>
      ) : (
        analysis
      )}

      {blocking && <p className="apk-blocked">This APK cannot be installed. Fix the problems above and add it again.</p>}
      {upload.phase === 'failed' && upload.error && <ErrorView error={upload.error} compact />}
    </div>
  );
}
