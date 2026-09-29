import { useEffect, useRef, useState, useSyncExternalStore } from 'react';
import { SplitPane } from './components/SplitPane';
import { Toasts } from './components/Toasts';
import { WindowDrop } from './features/apk/WindowDrop';
import { DetailsPanel } from './features/details/DetailsPanel';
import { AndroidPane } from './features/display/AndroidPane';
import { NetworkPanel } from './features/network/NetworkPanel';
import { EventPager } from './features/network/pager';
import { AppOnlyChip, Banners, ProgressBar, StatusPill, Toolbar } from './features/sandbox/Chrome';
import { Dialogs, ErrorDialog } from './features/sandbox/Dialogs';
import { SessionSelector } from './features/sessions/SessionSelector';
import { startApp } from './state/app';

function useMediaQuery(q: string): boolean {
  return useSyncExternalStore(
    (cb) => {
      const m = window.matchMedia?.(q);
      m?.addEventListener('change', cb);
      return () => m?.removeEventListener('change', cb);
    },
    () => window.matchMedia?.(q).matches ?? false,
  );
}

export function App() {
  const [pager] = useState(() => new EventPager());
  const details = useRef<HTMLElement>(null);
  const narrow = useMediaQuery('(max-width: 900px)');

  useEffect(() => startApp(), []);

  const openDetails = () => details.current?.querySelector<HTMLElement>('[role="tab"][aria-selected="true"]')?.focus();

  return (
    <div className="app">
      <header className="app-header">
        <h1 className="app-title">
          <img className="app-logo" src="./logo.png" alt="" width={34} height={34} />
          <span className="app-name">droidpector</span>
        </h1>
        <SessionSelector />
        <div className="header-right">
          <AppOnlyChip />
          <StatusPill />
        </div>
        <ProgressBar />
      </header>
      <Toolbar />
      <Banners />
      <main className="app-main">
        <SplitPane
          key={narrow ? 'v' : 'h'}
          direction={narrow ? 'vertical' : 'horizontal'}
          storageKey={narrow ? 'droidpector.split.main.narrow' : 'droidpector.split.main'}
          defaultFraction={narrow ? 0.45 : 0.28}
          minFirst={260}
          minSecond={360}
          label="Resize Android and Network panes"
          first={<AndroidPane />}
          second={
            <SplitPane
              direction="vertical"
              storageKey="droidpector.split.network"
              defaultFraction={0.55}
              minFirst={140}
              minSecond={120}
              label="Resize request list and details"
              first={<NetworkPanel pager={pager} onOpenDetails={openDetails} />}
              second={<DetailsPanel ref={details} />}
            />
          }
        />
      </main>
      <WindowDrop />
      <Dialogs />
      <ErrorDialog />
      <Toasts />
    </div>
  );
}
