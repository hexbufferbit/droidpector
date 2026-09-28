import { appStore } from '../state/app';
import { useStore } from '../state/store';
import { Icon } from './Icon';

export function Toasts() {
  const toasts = useStore(appStore, (s) => s.toasts);
  return (
    <div className="toasts" role="status" aria-live="polite">
      {toasts.map((t) => (
        <div key={t.id} className={`toast ${t.kind}`}>
          <Icon name={t.kind === 'error' ? 'error' : 'check'} size={13} />
          <span>{t.text}</span>
        </div>
      ))}
    </div>
  );
}
