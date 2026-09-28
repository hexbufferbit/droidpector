// A WebSocket that reconnects with exponential backoff. The host restarts
// the core after a crash, so the UI must survive the connection dropping.

export type SocketState = 'connecting' | 'open' | 'closed';

export interface ReconnectingSocketOptions {
  url: () => string;
  binaryType?: BinaryType;
  onMessage: (data: string | ArrayBuffer) => void;
  onState?: (state: SocketState) => void;
  onOpen?: (ws: WebSocket) => void;
  minDelay?: number;
  maxDelay?: number;
}

/** backoffDelay returns the reconnect delay for the given attempt (0-based). */
export function backoffDelay(attempt: number, min = 500, max = 10_000): number {
  return Math.min(max, min * 2 ** Math.min(attempt, 16));
}

export class ReconnectingSocket {
  private ws: WebSocket | null = null;
  private attempt = 0;
  private timer: ReturnType<typeof setTimeout> | null = null;
  private stopped = false;
  private readonly opts: ReconnectingSocketOptions;

  constructor(opts: ReconnectingSocketOptions) {
    this.opts = opts;
    this.connect();
  }

  private connect(): void {
    if (this.stopped) return;
    this.opts.onState?.('connecting');
    let ws: WebSocket;
    try {
      ws = new WebSocket(this.opts.url());
    } catch {
      this.schedule();
      return;
    }
    ws.binaryType = this.opts.binaryType ?? 'arraybuffer';
    this.ws = ws;
    ws.onopen = () => {
      this.attempt = 0;
      this.opts.onState?.('open');
      this.opts.onOpen?.(ws);
    };
    ws.onmessage = (ev: MessageEvent<string | ArrayBuffer>) => this.opts.onMessage(ev.data);
    ws.onclose = () => {
      if (this.ws !== ws) return;
      this.ws = null;
      this.opts.onState?.('closed');
      this.schedule();
    };
    ws.onerror = () => {
      // onclose follows and schedules the reconnect
    };
  }

  private schedule(): void {
    if (this.stopped || this.timer) return;
    const delay = backoffDelay(this.attempt++, this.opts.minDelay, this.opts.maxDelay);
    this.timer = setTimeout(() => {
      this.timer = null;
      this.connect();
    }, delay);
  }

  send(data: string): boolean {
    if (this.ws && this.ws.readyState === WebSocket.OPEN) {
      this.ws.send(data);
      return true;
    }
    return false;
  }

  get open(): boolean {
    return !!this.ws && this.ws.readyState === WebSocket.OPEN;
  }

  close(): void {
    this.stopped = true;
    if (this.timer) clearTimeout(this.timer);
    this.timer = null;
    const ws = this.ws;
    this.ws = null;
    if (ws) {
      ws.onclose = null;
      ws.close();
    }
  }
}
