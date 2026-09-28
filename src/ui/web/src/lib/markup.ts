// Minimal XML/HTML pretty-printer and tokenizer for syntax highlighting.
// Markup is only ever shown as source text; it is never rendered as a page.

export type MarkupTokenType = 'tag' | 'attr' | 'value' | 'punct' | 'comment' | 'text' | 'doctype';

export interface MarkupToken {
  type: MarkupTokenType;
  text: string;
}

const VOID = new Set(['area', 'base', 'br', 'col', 'embed', 'hr', 'img', 'input', 'link', 'meta', 'param', 'source', 'track', 'wbr']);
const RAW = new Set(['script', 'style', 'pre', 'textarea']);

/** formatMarkup re-indents markup: one tag per line, nested content indented. */
export function formatMarkup(src: string, indent = '  '): string {
  const parts = src.match(/<!--[\s\S]*?-->|<!\[CDATA\[[\s\S]*?\]\]>|<[^>]+>|[^<]+/g);
  if (!parts) return src;
  const out: string[] = [];
  let depth = 0;
  const pad = () => indent.repeat(depth);
  for (let i = 0; i < parts.length; i++) {
    const part = parts[i];
    if (part.startsWith('<!') || part.startsWith('<?')) {
      out.push(pad() + part.trim());
    } else if (part.startsWith('</')) {
      depth = Math.max(0, depth - 1);
      out.push(pad() + part);
    } else if (part.startsWith('<')) {
      const name = /^<([\w:.-]+)/.exec(part)?.[1]?.toLowerCase() ?? '';
      if (part.endsWith('/>') || VOID.has(name)) {
        out.push(pad() + part);
        continue;
      }
      if (RAW.has(name)) {
        // Copy script/style/pre content verbatim up to the closing tag.
        let j = i + 1;
        let body = '';
        while (j < parts.length && !new RegExp(`^</${name}\\s*>$`, 'i').test(parts[j])) body += parts[j++];
        out.push(pad() + part);
        if (body.trim()) for (const line of body.replace(/^\n+|\s+$/g, '').split('\n')) out.push(pad() + indent + line);
        if (j < parts.length) out.push(pad() + parts[j]);
        i = j;
        continue;
      }
      // <tag>short text</tag> stays on one line.
      const text = parts[i + 1];
      const close = parts[i + 2];
      if (text !== undefined && close !== undefined && !text.startsWith('<') && close.startsWith('</')) {
        const t = text.trim();
        if (t.length < 100 && !t.includes('\n')) {
          out.push(pad() + part + t + close);
          i += 2;
          continue;
        }
      }
      out.push(pad() + part);
      depth++;
    } else {
      const t = part.trim();
      if (t) for (const line of t.split('\n')) out.push(pad() + line.trim());
    }
  }
  return out.join('\n');
}

/** tokenizeMarkup splits markup into highlightable tokens (lossless: tokens concatenate to src). */
export function tokenizeMarkup(src: string): MarkupToken[] {
  const tokens: MarkupToken[] = [];
  const re = /<!--[\s\S]*?-->|<![^>]*>|<\?[\s\S]*?\?>|<\/?[^>]*>|[^<]+|</g;
  let m: RegExpExecArray | null;
  while ((m = re.exec(src))) {
    const s = m[0];
    if (s.startsWith('<!--')) tokens.push({ type: 'comment', text: s });
    else if (s.startsWith('<!') || s.startsWith('<?')) tokens.push({ type: 'doctype', text: s });
    else if (s.startsWith('<') && s.length > 1) tokenizeTag(s, tokens);
    else tokens.push({ type: 'text', text: s });
  }
  return tokens;
}

function tokenizeTag(s: string, out: MarkupToken[]): void {
  const m = /^(<\/?)([^\s/>]*)/.exec(s);
  if (!m) {
    out.push({ type: 'text', text: s });
    return;
  }
  out.push({ type: 'punct', text: m[1] });
  if (m[2]) out.push({ type: 'tag', text: m[2] });
  let rest = s.slice(m[0].length);
  const attrRe = /^(\s+)|^([^\s=/>"']+)|^(=)|^("[^"]*"?|'[^']*'?)|^(\/?>)|^([\s\S])/;
  while (rest) {
    const a = attrRe.exec(rest);
    if (!a) break;
    if (a[1]) out.push({ type: 'text', text: a[1] });
    else if (a[2]) out.push({ type: 'attr', text: a[2] });
    else if (a[3]) out.push({ type: 'punct', text: a[3] });
    else if (a[4]) out.push({ type: 'value', text: a[4] });
    else if (a[5]) out.push({ type: 'punct', text: a[5] });
    else out.push({ type: 'text', text: a[6] });
    rest = rest.slice(a[0].length);
  }
}
