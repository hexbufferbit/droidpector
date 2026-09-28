import { appStore } from '../state/app';
import { useStore } from '../state/store';

export function Toasts() {
  const toasts = useStore(appStore, (s) => s.toasts);
  return (
    <div className="toasts" role="status" aria-live="polite">
      {toasts.map((t) => (
        <div key={t.id} className={`toast ${t.kind}`}>
          {t.text}
        </div>
      ))}
    </div>
  );
}
