// Inline SVG icons (offline: no icon fonts or external assets).
import type { SVGProps } from 'react';

const PATHS = {
  lock: 'M5 7V5a3 3 0 0 1 6 0v2h1v7H4V7h1zm1.5 0h3V5a1.5 1.5 0 0 0-3 0v2z',
  replay: 'M8 3a5 5 0 1 1-4.9 6h1.5A3.5 3.5 0 1 0 8 4.5V7L4.5 3.8 8 .5V3z',
  up: 'M8 2l5 6H9.5v6h-3V8H3z',
  down: 'M8 14l-5-6h3.5V2h3v6H13z',
  help: 'M8 1a7 7 0 1 1 0 14A7 7 0 0 1 8 1zm0 10.2a1 1 0 1 0 0 2 1 1 0 0 0 0-2zM8 3.5c-1.6 0-2.8 1-2.9 2.5h1.6c.1-.7.6-1.1 1.3-1.1.8 0 1.3.5 1.3 1.1 0 .5-.3.9-1 1.3-.9.5-1.2 1.1-1.2 2v.3h1.6v-.2c0-.6.2-.9 1-1.3.9-.5 1.3-1.2 1.3-2.1 0-1.5-1.2-2.5-3-2.5z',
  close: 'M3.5 2.4L8 6.9l4.5-4.5 1.1 1.1L9.1 8l4.5 4.5-1.1 1.1L8 9.1l-4.5 4.5-1.1-1.1L6.9 8 2.4 3.5z',
  more: 'M3 6.5a1.5 1.5 0 1 1 0 3 1.5 1.5 0 0 1 0-3zm5 0a1.5 1.5 0 1 1 0 3 1.5 1.5 0 0 1 0-3zm5 0a1.5 1.5 0 1 1 0 3 1.5 1.5 0 0 1 0-3z',
  play: 'M4 2.5l9 5.5-9 5.5z',
  stop: 'M3.5 3.5h9v9h-9z',
  restart: 'M8 2.5a5.5 5.5 0 1 0 5.4 6.5h-1.6A4 4 0 1 1 8 4v2.5L11.5 3 8 0z',
  upload: 'M8 1l4 4.5H9.5V10h-3V5.5H4zM2.5 11h1.5v2h8v-2h1.5v3.5h-11z',
  back: 'M6.5 3L1.5 8l5 5V9.5h8v-3h-8z',
  home: 'M8 1.5l6.5 6H12.5V14h-3.5v-4h-2v4H3.5V7.5H1.5z',
  paste: 'M6 1h4v1.5h2.5V15h-9V2.5H6zm1 1.5v1h2v-1zM5 4v9.5h6V4h-.5v1.5h-5V4z',
  copy: 'M5 1h8v10H5zm1.5 1.5v7h5v-7zM2.5 4H4v9.5h7V15H2.5z',
  search: 'M6.5 1.5a5 5 0 0 1 4 8l3.5 3.5-1.1 1.1-3.5-3.5a5 5 0 1 1-2.9-9.1zm0 1.5a3.5 3.5 0 1 0 0 7 3.5 3.5 0 0 0 0-7z',
  chevronRight: 'M6 3l5 5-5 5-1.1-1.1L8.8 8 4.9 4.1z',
  chevronDown: 'M3 6l5 5 5-5-1.1-1.1L8 8.8 4.1 4.9z',
  download: 'M6.5 1h3v5.5H12L8 11 4 6.5h2.5zM2.5 12.5h11V15h-11z',
  warning: 'M8 1l7 13H1zm-.8 4.5v4.2h1.6V5.5zm.8 5.3a.9.9 0 1 0 0 1.8.9.9 0 0 0 0-1.8z',
  error: 'M8 1a7 7 0 1 1 0 14A7 7 0 0 1 8 1zm-.8 3.5v5h1.6v-5zm.8 6a.9.9 0 1 0 0 1.8.9.9 0 0 0 0-1.8z',
  android: 'M4 6h8v6.5a1 1 0 0 1-1 1h-.5V15h-1.5v-1.5h-2V15H5.5v-1.5H5a1 1 0 0 1-1-1zm0-1a4 4 0 0 1 8 0zm2.5-1.8a.6.6 0 1 0 0 1.2.6.6 0 0 0 0-1.2zm3 0a.6.6 0 1 0 0 1.2.6.6 0 0 0 0-1.2zM2 6.5h1.3v4.5H2zm10.7 0H14v4.5h-1.3z',
  camera: 'M5.5 2.5h5l1 1.5H14v9.5H2V4h2.5zM8 5.5a2.75 2.75 0 1 0 0 5.5 2.75 2.75 0 0 0 0-5.5z',
  trash: 'M5.5 1h5v1.5H14V4H2V2.5h3.5zM3.5 5h9l-.7 10H4.2z',
  save: 'M2 2h9.5L14 4.5V14H2zm2 1.5v3h6v-3zM5 9.5v3h6v-3z',
  plus: 'M7.2 2h1.6v5.2H14v1.6H8.8V14H7.2V8.8H2V7.2h5.2z',
  filter: 'M1.5 2h13L9.5 8v5l-3 1.5V8z',
} as const;

export type IconName = keyof typeof PATHS;

export function Icon({ name, size = 14, ...rest }: { name: IconName; size?: number } & SVGProps<SVGSVGElement>) {
  return (
    <svg width={size} height={size} viewBox="0 0 16 16" fill="currentColor" aria-hidden="true" focusable="false" className="icon" {...rest}>
      <path d={PATHS[name]} fillRule="evenodd" />
    </svg>
  );
}
