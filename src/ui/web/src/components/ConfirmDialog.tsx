import type { ReactNode } from 'react';
import { Dialog } from './Dialog';

export function ConfirmDialog({
  title,
  children,
  confirmLabel,
  danger = false,
  onConfirm,
  onClose,
}: {
  title: string;
  children: ReactNode;
  confirmLabel: string;
  danger?: boolean;
  onConfirm: () => void;
  onClose: () => void;
}) {
  return (
    <Dialog
      title={title}
      onClose={onClose}
      tone={danger ? 'danger' : 'default'}
      width={440}
      footer={
        <>
          <button className="btn" onClick={onClose} data-autofocus>
            Cancel
          </button>
          <button
            className={`btn ${danger ? 'danger' : 'primary'}`}
            onClick={() => {
              onClose();
              onConfirm();
            }}
          >
            {confirmLabel}
          </button>
        </>
      }
    >
      {children}
    </Dialog>
  );
}
