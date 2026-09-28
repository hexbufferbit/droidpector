import { useEffect, useRef, useState } from 'react';
import { Icon } from '../../components/Icon';
import { actions } from '../../state/app';
import { dragHasFiles, firstApk } from './files';

/** WindowDrop accepts APK files dropped anywhere on the window. */
export function WindowDrop() {
  const [active, setActive] = useState(false);
  const depth = useRef(0);
  useEffect(() => {
    const enter = (e: DragEvent) => {
      if (!dragHasFiles(e.dataTransfer)) return;
      depth.current++;
      setActive(true);
    };
    const leave = () => {
      depth.current = Math.max(0, depth.current - 1);
      if (depth.current === 0) setActive(false);
    };
    const over = (e: DragEvent) => {
      if (!dragHasFiles(e.dataTransfer)) return;
      e.preventDefault();
      if (e.dataTransfer) e.dataTransfer.dropEffect = 'copy';
    };
    const drop = (e: DragEvent) => {
      depth.current = 0;
      setActive(false);
      if (!dragHasFiles(e.dataTransfer)) return;
      e.preventDefault();
      const f = firstApk(e.dataTransfer?.files);
      if (f) void actions.uploadApk(f);
    };
    window.addEventListener('dragenter', enter);
    window.addEventListener('dragleave', leave);
    window.addEventListener('dragover', over);
    window.addEventListener('drop', drop);
    return () => {
      window.removeEventListener('dragenter', enter);
      window.removeEventListener('dragleave', leave);
      window.removeEventListener('dragover', over);
      window.removeEventListener('drop', drop);
    };
  }, []);
  if (!active) return null;
  return (
    <div className="window-drop" aria-hidden="true">
      <div className="window-drop-inner">
        <Icon name="upload" size={40} />
        <p>Drop the APK to install and run it</p>
      </div>
    </div>
  );
}
