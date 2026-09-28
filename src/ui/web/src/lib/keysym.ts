// KeyboardEvent → X11 keysym mapping for the RFB (VNC) display input.

export const XK = {
  BackSpace: 0xff08,
  Tab: 0xff09,
  Return: 0xff0d,
  Escape: 0xff1b,
  Delete: 0xffff,
  Home: 0xff50,
  Left: 0xff51,
  Up: 0xff52,
  Right: 0xff53,
  Down: 0xff54,
  Page_Up: 0xff55,
  Page_Down: 0xff56,
  End: 0xff57,
  Insert: 0xff63,
  Menu: 0xff67,
  Num_Lock: 0xff7f,
  KP_Enter: 0xff8d,
  F1: 0xffbe,
  Shift_L: 0xffe1,
  Shift_R: 0xffe2,
  Control_L: 0xffe3,
  Control_R: 0xffe4,
  Caps_Lock: 0xffe5,
  Meta_L: 0xffe7,
  Meta_R: 0xffe8,
  Alt_L: 0xffe9,
  Alt_R: 0xffea,
  ISO_Level3_Shift: 0xfe03,
  Pause: 0xff13,
  Scroll_Lock: 0xff14,
  Print: 0xff61,
} as const;

const LOCATION_RIGHT = 2;
const LOCATION_NUMPAD = 3;

const NAMED: Record<string, number> = {
  Backspace: XK.BackSpace,
  Tab: XK.Tab,
  Enter: XK.Return,
  Escape: XK.Escape,
  Esc: XK.Escape,
  Delete: XK.Delete,
  Del: XK.Delete,
  Home: XK.Home,
  End: XK.End,
  PageUp: XK.Page_Up,
  PageDown: XK.Page_Down,
  ArrowLeft: XK.Left,
  ArrowUp: XK.Up,
  ArrowRight: XK.Right,
  ArrowDown: XK.Down,
  Left: XK.Left,
  Up: XK.Up,
  Right: XK.Right,
  Down: XK.Down,
  Insert: XK.Insert,
  ContextMenu: XK.Menu,
  CapsLock: XK.Caps_Lock,
  NumLock: XK.Num_Lock,
  AltGraph: XK.ISO_Level3_Shift,
  Pause: XK.Pause,
  ScrollLock: XK.Scroll_Lock,
  PrintScreen: XK.Print,
};

const MODIFIERS: Record<string, [number, number]> = {
  Shift: [XK.Shift_L, XK.Shift_R],
  Control: [XK.Control_L, XK.Control_R],
  Alt: [XK.Alt_L, XK.Alt_R],
  Meta: [XK.Meta_L, XK.Meta_R],
  OS: [XK.Meta_L, XK.Meta_R],
};

/** keysymForChar maps one Unicode character to its keysym (Latin-1 = code point, else 0x01000000 + cp). */
export function keysymForChar(ch: string): number | null {
  const cp = ch.codePointAt(0);
  if (cp === undefined) return null;
  if (cp < 0x20 || (cp >= 0x7f && cp < 0xa0)) return null; // control characters
  if (cp <= 0xff) return cp;
  return 0x01000000 + cp;
}

export interface KeyLike {
  key: string;
  code?: string;
  location?: number;
}

/** keysymFromEvent returns the X11 keysym for a KeyboardEvent, or null when unmapped. */
export function keysymFromEvent(e: KeyLike): number | null {
  const { key } = e;
  if (!key || key === 'Unidentified' || key === 'Dead' || key === 'Process') return null;
  const mod = MODIFIERS[key];
  if (mod) return e.location === LOCATION_RIGHT ? mod[1] : mod[0];
  if (key === 'Enter' && e.location === LOCATION_NUMPAD) return XK.KP_Enter;
  const named = NAMED[key];
  if (named !== undefined) return named;
  const fm = /^F([1-9]|1[0-2])$/.exec(key);
  if (fm) return XK.F1 + Number(fm[1]) - 1;
  // Printable: exactly one code point.
  if ([...key].length === 1) return keysymForChar(key);
  return null;
}
