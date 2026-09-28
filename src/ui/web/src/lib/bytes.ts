// Byte/text helpers.

export function base64ToBytes(b64: string | undefined): Uint8Array {
  if (!b64) return new Uint8Array();
  try {
    const bin = atob(b64);
    const out = new Uint8Array(bin.length);
    for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
    return out;
  } catch {
    return new Uint8Array();
  }
}

export function decodeUtf8(bytes: Uint8Array): string {
  return new TextDecoder('utf-8', { fatal: false }).decode(bytes);
}

/** isProbablyText reports whether bytes decode as UTF-8 without many control characters. */
export function isProbablyText(bytes: Uint8Array): boolean {
  const sample = bytes.subarray(0, 4096);
  try {
    new TextDecoder('utf-8', { fatal: true }).decode(sample.length === bytes.length ? sample : bytes.subarray(0, 4096 - 4));
  } catch {
    return false;
  }
  let ctrl = 0;
  for (const b of sample) if (b < 0x09 || (b > 0x0d && b < 0x20)) ctrl++;
  return ctrl <= sample.length / 50;
}
