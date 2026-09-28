// Inline SVG icons (offline: no icon fonts or external assets). 16×16 grid.
import type { SVGProps } from 'react';

const PATHS = {
  lock: 'M5 7V5a3 3 0 0 1 6 0v2h1v7H4V7h1zm1.5 0h3V5a1.5 1.5 0 0 0-3 0v2z',
  replay: 'M8 3a5 5 0 1 1-4.9 6h1.5A3.5 3.5 0 1 0 8 4.5V7L4.5 3.8 8 .5V3z',
  up: 'M8 2l5 6H9.5v6h-3V8H3z',
  down: 'M8 14l-5-6h3.5V2h3v6H13z',
  help: 'M8 1a7 7 0 1 1 0 14A7 7 0 0 1 8 1zm0 10.2a1 1 0 1 0 0 2 1 1 0 0 0 0-2zM8 3.5c-1.6 0-2.8 1-2.9 2.5h1.6c.1-.7.6-1.1 1.3-1.1.8 0 1.3.5 1.3 1.1 0 .5-.3.9-1 1.3-.9.5-1.2 1.1-1.2 2v.3h1.6v-.2c0-.6.2-.9 1-1.3.9-.5 1.3-1.2 1.3-2.1 0-1.5-1.2-2.5-3-2.5z',
  close: 'M3.5 2.4L8 6.9l4.5-4.5 1.1 1.1L9.1 8l4.5 4.5-1.1 1.1L8 9.1l-4.5 4.5-1.1-1.1L6.9 8 2.4 3.5z',
  more: 'M3 6.5a1.5 1.5 0 1 1 0 3 1.5 1.5 0 0 1 0-3zm5 0a1.5 1.5 0 1 1 0 3 1.5 1.5 0 0 1 0-3zm5 0a1.5 1.5 0 1 1 0 3 1.5 1.5 0 0 1 0-3z',
  play: 'M4.5 2.8a.8.8 0 0 1 1.2-.7l7.5 5.2a.8.8 0 0 1 0 1.4l-7.5 5.2a.8.8 0 0 1-1.2-.7z',
  stop: 'M3.5 4.5a1 1 0 0 1 1-1h7a1 1 0 0 1 1 1v7a1 1 0 0 1-1 1h-7a1 1 0 0 1-1-1z',
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
  info: 'M8 1a7 7 0 1 1 0 14A7 7 0 0 1 8 1zm-.8 5.5v5h1.6v-5zm.8-3a.9.9 0 1 0 0 1.8.9.9 0 0 0 0-1.8z',
  check: 'M6.4 11.6L2.6 7.8l1.2-1.2 2.6 2.6 5.8-5.8 1.2 1.2z',
  android: 'M4 6h8v6.5a1 1 0 0 1-1 1h-.5V15h-1.5v-1.5h-2V15H5.5v-1.5H5a1 1 0 0 1-1-1zm0-1a4 4 0 0 1 8 0zm2.5-1.8a.6.6 0 1 0 0 1.2.6.6 0 0 0 0-1.2zm3 0a.6.6 0 1 0 0 1.2.6.6 0 0 0 0-1.2zM2 6.5h1.3v4.5H2zm10.7 0H14v4.5h-1.3z',
  camera: 'M5.5 2.5h5l1 1.5H14v9.5H2V4h2.5zM8 5.5a2.75 2.75 0 1 0 0 5.5 2.75 2.75 0 0 0 0-5.5z',
  trash: 'M5.5 1h5v1.5H14V4H2V2.5h3.5zM3.5 5h9l-.7 10H4.2z',
  save: 'M2 2h9.5L14 4.5V14H2zm2 1.5v3h6v-3zM5 9.5v3h6v-3z',
  plus: 'M7.2 2h1.6v5.2H14v1.6H8.8V14H7.2V8.8H2V7.2h5.2z',
  filter: 'M1.5 2h13L9.5 8v5l-3 1.5V8z',
  shield: 'M8 1l5.5 2v4.2c0 3.3-2.3 6.3-5.5 7.3C4.8 13.5 2.5 10.5 2.5 7.2V3zm0 1.6L4 4v3.2c0 2.5 1.6 4.8 4 5.7 2.4-.9 4-3.2 4-5.7V4z',
  shieldCheck: 'M8 1l5.5 2v4.2c0 3.3-2.3 6.3-5.5 7.3C4.8 13.5 2.5 10.5 2.5 7.2V3zM7.1 10.3l3.9-3.9-1.1-1.1-2.8 2.8-1.3-1.3-1.1 1.1z',
  settings: 'M6.6 1h2.8l.4 1.8 1.2.7 1.7-.6 1.4 2.4-1.3 1.2v1.4l1.3 1.2-1.4 2.4-1.7-.6-1.2.7-.4 1.8H6.6l-.4-1.8-1.2-.7-1.7.6L1.9 8.7l1.3-1.2V6.1L1.9 4.9l1.4-2.4 1.7.6 1.2-.7zM8 5.5a2.5 2.5 0 1 0 0 5 2.5 2.5 0 0 0 0-5z',
  phone: 'M4.5 1h7a1 1 0 0 1 1 1v12a1 1 0 0 1-1 1h-7a1 1 0 0 1-1-1V2a1 1 0 0 1 1-1zM5 3v9h6V3zm2 10a1 1 0 1 0 2 0 1 1 0 0 0-2 0z',
  globe: 'M8 1a7 7 0 1 1 0 14A7 7 0 0 1 8 1zm-.8 1.6C6 3.7 5.2 5.6 5.1 7.2h2.1zm1.6 0v4.6h2.1c-.1-1.6-.9-3.5-2.1-4.6zM5.1 8.8c.1 1.6.9 3.5 2.1 4.6V8.8zm3.7 0v4.6c1.2-1.1 2-3 2.1-4.6zM3.5 7.2c.1-1.3.5-2.5 1.1-3.5A5.5 5.5 0 0 0 2.6 7.2zm0 1.6H2.6a5.5 5.5 0 0 0 2 3.5c-.6-1-1-2.2-1.1-3.5zm9-1.6h.9a5.5 5.5 0 0 0-2-3.5c.6 1 1 2.2 1.1 3.5zm0 1.6c-.1 1.3-.5 2.5-1.1 3.5a5.5 5.5 0 0 0 2-3.5z',
  inbox: 'M2 2h12v12H2zm1.5 1.5v6H6a2 2 0 0 0 4 0h2.5v-6zm0 7.5v1.5h9V11h-1.3a3.5 3.5 0 0 1-6.4 0z',
  cursor: 'M3 1.5l10 6-4.2 1.1 2.4 4.2-1.8 1-2.4-4.2L4 12.6z',
  zap: 'M9 1L3 9h4l-1 6 6-8H8z',
  link: 'M6.4 9.6a3 3 0 0 0 4.2 0l2.1-2.1a3 3 0 0 0-4.2-4.2l-.9.9 1.1 1.1.9-.9a1.5 1.5 0 0 1 2.1 2.1l-2.1 2.1a1.5 1.5 0 0 1-2.1 0zm3.2-3.2a3 3 0 0 0-4.2 0L3.3 8.5a3 3 0 0 0 4.2 4.2l.9-.9-1.1-1.1-.9.9a1.5 1.5 0 0 1-2.1-2.1l2.1-2.1a1.5 1.5 0 0 1 2.1 0z',
  clock: 'M8 1a7 7 0 1 1 0 14A7 7 0 0 1 8 1zm0 1.5a5.5 5.5 0 1 0 0 11 5.5 5.5 0 0 0 0-11zM7.2 4h1.6v4.2l2.8 1.6-.8 1.4-3.6-2.1z',
} as const;

export type IconName = keyof typeof PATHS;

export function Icon({ name, size = 14, ...rest }: { name: IconName; size?: number } & SVGProps<SVGSVGElement>) {
  return (
    <svg width={size} height={size} viewBox="0 0 16 16" fill="currentColor" aria-hidden="true" focusable="false" className="icon" {...rest}>
      <path d={PATHS[name]} fillRule="evenodd" />
    </svg>
  );
}

/** Illustration is a larger decorative glyph for empty states (a soft disc behind an icon). */
export function Illustration({ name, size = 56 }: { name: IconName; size?: number }) {
  return (
    <span className="illustration" style={{ width: size, height: size }} aria-hidden="true">
      <Icon name={name} size={Math.round(size * 0.46)} />
    </span>
  );
}
