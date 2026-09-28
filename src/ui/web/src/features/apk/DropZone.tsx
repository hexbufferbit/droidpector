import { useState } from 'react';
import { Icon } from '../../components/Icon';
import { actions } from '../../state/app';
import { dragHasFiles, firstApk, pickApk } from './files';

/** DropZone is the empty state shown inside the phone screen: drop an APK or choose a file. */
export function DropZone() {
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
      <img className="drop-logo" src="./logo.png" alt="" width={96} height={96} draggable={false} />
      <p className="drop-title">Drop an APK here</p>
      <p className="drop-sub">It is installed in an isolated Android sandbox and every request it makes shows up on the right.</p>
      <button className="btn primary pill" onClick={() => pickApk((f) => void actions.uploadApk(f))}>
        <Icon name="upload" /> Choose APK file…
      </button>
    </div>
  );
}
