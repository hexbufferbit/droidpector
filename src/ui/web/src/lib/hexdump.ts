// Hex dump formatting ("offset | hex | ascii") for binary bodies.

export const HEXDUMP_LIMIT = 64 * 1024;

function hex(n: number, width: number): string {
  return n.toString(16).padStart(width, '0');
}

/** hexdumpLine formats 16 bytes starting at offset. */
export function hexdumpLine(bytes: Uint8Array, offset: number, width = 16): string {
  const end = Math.min(bytes.length, offset + width);
  let h = '';
  let a = '';
  for (let i = offset; i < offset + width; i++) {
    if (i < end) {
      const b = bytes[i];
      h += hex(b, 2) + ' ';
      a += b >= 0x20 && b < 0x7f ? String.fromCharCode(b) : '.';
    } else {
      h += '   ';
    }
    if (i - offset === width / 2 - 1) h += ' ';
  }
  return `${hex(offset, 8)}  ${h} |${a}|`;
}

export interface Hexdump {
  text: string;
  lines: number;
  shown: number;
  truncated: boolean;
}

/** hexdump formats up to limit bytes. */
export function hexdump(bytes: Uint8Array, limit = HEXDUMP_LIMIT): Hexdump {
  const shown = Math.min(bytes.length, limit);
  const out: string[] = [];
  const view = bytes.subarray(0, shown);
  for (let off = 0; off < shown; off += 16) out.push(hexdumpLine(view, off));
  return { text: out.join('\n'), lines: out.length, shown, truncated: shown < bytes.length };
}
