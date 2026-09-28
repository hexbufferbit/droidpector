import { useState } from 'react';
import { Icon } from '../../components/Icon';
import { actions, appStore } from '../../state/app';
import { dragHasFiles, firstApk, pickApk } from './files';

/** DropZone is the empty state of the Android pane: drop an APK or choose a file. */
export function DropZone({ canShowAndroid }: { canShowAndroid: boolean }) {
  const [over, setOver] = useState(false);
  return (
    <div
      className={`drop-zone${over ? ' over' : ''}`}
      onDragOver={(e) => {
        if (!dragHasFiles(e.dataTransfer)) return;
        e.preventDefault();
        e.stopPropagation();
        e.dataTransfer.dropEffect = 'copy';
        setOver(true);
      }}
      onDragLeave={() => setOver(false)}
      onDrop={(e) => {
        e.preventDefault();
        e.stopPropagation();
        setOver(false);
        const f = firstApk(e.dataTransfer.files);
        if (f) void actions.uploadApk(f);
      }}
    >
      <Icon name="android" size={48} />
      <p className="drop-title">Drop an APK here or choose a file</p>
      <p className="muted">The app is installed in an isolated Android sandbox and its network traffic appears on the right.</p>
      <button className="btn primary" onClick={() => pickApk((f) => void actions.uploadApk(f))}>
        <Icon name="upload" /> Choose APK file…
      </button>
      {canShowAndroid && (
        <button className="link-btn" onClick={() => appStore.set({ hideDropZone: true })}>
          Show the Android screen instead
        </button>
      )}
    </div>
  );
}

