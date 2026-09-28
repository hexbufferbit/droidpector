// Clipboard helpers: async Clipboard API with a textarea fallback.

export async function copyText(text: string): Promise<void> {
  try {
    if (navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(text);
      return;
    }
  } catch {
    // fall back below (permission denied, insecure context, ...)
  }
  const ta = document.createElement('textarea');
  ta.value = text;
  ta.setAttribute('readonly', '');
  ta.style.position = 'fixed';
  ta.style.opacity = '0';
  ta.style.left = '-9999px';
  document.body.appendChild(ta);
  ta.select();
  try {
    if (!document.execCommand('copy')) throw new Error('copy command failed');
  } finally {
    document.body.removeChild(ta);
  }
}

export async function readClipboardText(): Promise<string> {
  if (!navigator.clipboard?.readText) throw new Error('Reading the clipboard is not supported here.');
  return navigator.clipboard.readText();
}
