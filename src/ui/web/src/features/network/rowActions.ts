// Actions offered by the Network row context menu.
import { api, links } from '../../api/client';
import type { Summary } from '../../api/types';
import { copyText } from '../../lib/clipboard';
import { decodeUtf8 } from '../../lib/bytes';
import { summaryUrl } from '../../lib/url';
import { actions, revealEvent, showError, toast } from '../../state/app';
import { filterStore } from '../../state/filterState';

export function downloadHref(href: string): void {
  const a = document.createElement('a');
  a.href = href;
  a.download = '';
  a.rel = 'noopener';
  document.body.appendChild(a);
  a.click();
  a.remove();
}

async function copy(what: string, produce: () => Promise<string> | string): Promise<void> {
  try {
    const text = await produce();
    await copyText(text);
    toast(`Copied ${what}`);
  } catch (err) {
    showError(err);
  }
}

export const rowActions = {
  copyCode: (id: string, gen: { id: string; label: string }) =>
    copy(gen.label.replace(/^Copy\s+/i, ''), () => api.code(id, gen.id)),

  copyUrl: (s: Summary) => copy('URL', () => summaryUrl(s)),

  copyHeaders: (id: string) =>
    copy('request headers', async () => {
      const d = await api.event(id);
      return (d.requestHeaders ?? []).map((h) => `${h.name}: ${h.value}`).join('\n');
    }),

  copyResponse: (id: string) =>
    copy('response', async () => {
      const b = await api.body(id, 'response');
      return decodeUtf8(b.bytes);
    }),

  async replay(id: string): Promise<void> {
    try {
      const s = await api.replay(id);
      toast('Request replayed');
      actions.selectEvent(s.id);
      revealEvent.emit(s.id);
    } catch (err) {
      showError(err);
    }
  },

  saveHar: (id: string) => downloadHref(links.eventHar(id)),

  filterByHost(host: string) {
    const h = /[\s"()]/.test(host) ? `"${host.replace(/"/g, '\\"')}"` : host;
    filterStore.set({ filter: `host:${h}` });
  },
};
