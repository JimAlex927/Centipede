type EventHandler = (...args: any[]) => void;

export interface RealtimeSocket {
  connected: boolean;
  connect(): void;
  disconnect(): void;
  on(event: string, handler: EventHandler): this;
  off(event: string, handler?: EventHandler): this;
  emit(event: string, data?: unknown): this;
}

type WireEnvelope = {
  event: string;
  data?: unknown;
};

// Small Socket.IO-compatible surface used by Docmost's existing subscriptions.
// The wire protocol is deliberately plain JSON so the standalone client talks
// directly to the Go WebSocket endpoint without a Node Socket.IO server.
export class RealtimeClient implements RealtimeSocket {
  connected = false;
  private socket: WebSocket | null = null;
  private manuallyDisconnected = false;
  private reconnectTimer: ReturnType<typeof setTimeout> | null = null;
  private reconnectDelay = 500;
  private pending: string[] = [];
  private handlers = new Map<string, Set<EventHandler>>();

  constructor(private readonly url: string) {}

  connect(): void {
    this.manuallyDisconnected = false;
    if (this.socket && this.socket.readyState <= WebSocket.OPEN) return;
    if (this.reconnectTimer) {
      clearTimeout(this.reconnectTimer);
      this.reconnectTimer = null;
    }

    const socket = new WebSocket(this.url);
    this.socket = socket;
    socket.addEventListener("open", () => {
      if (this.socket !== socket) return;
      this.connected = true;
      this.reconnectDelay = 500;
      this.flushPending();
      this.dispatch("connect");
    });
    socket.addEventListener("message", (event) => {
      if (this.socket !== socket) return;
      try {
        const envelope = JSON.parse(String(event.data)) as WireEnvelope;
        if (!envelope?.event) return;
        this.dispatch(envelope.event, envelope.data);
      } catch {
        // Ignore malformed messages from the server.
      }
    });
    socket.addEventListener("close", () => {
      if (this.socket !== socket) return;
      this.socket = null;
      const wasConnected = this.connected;
      this.connected = false;
      if (wasConnected) this.dispatch("disconnect");
      if (!this.manuallyDisconnected) this.scheduleReconnect();
    });
    socket.addEventListener("error", () => {
      // close will perform the reconnect and avoids duplicate timers.
    });
  }

  disconnect(): void {
    this.manuallyDisconnected = true;
    if (this.reconnectTimer) {
      clearTimeout(this.reconnectTimer);
      this.reconnectTimer = null;
    }
    this.pending = [];
    this.socket?.close();
    this.socket = null;
    this.connected = false;
  }

  on(event: string, handler: EventHandler): this {
    const handlers = this.handlers.get(event) ?? new Set<EventHandler>();
    handlers.add(handler);
    this.handlers.set(event, handlers);
    return this;
  }

  off(event: string, handler?: EventHandler): this {
    if (!handler) {
      this.handlers.delete(event);
      return this;
    }
    const handlers = this.handlers.get(event);
    handlers?.delete(handler);
    if (handlers?.size === 0) this.handlers.delete(event);
    return this;
  }

  emit(event: string, data?: unknown): this {
    const payload = JSON.stringify({ event, data } satisfies WireEnvelope);
    if (this.socket?.readyState === WebSocket.OPEN) {
      this.socket.send(payload);
    } else if (!this.manuallyDisconnected) {
      this.pending.push(payload);
      this.connect();
    }
    return this;
  }

  private dispatch(event: string, ...args: any[]): void {
    for (const handler of this.handlers.get(event) ?? []) handler(...args);
  }

  private flushPending(): void {
    if (!this.socket || this.socket.readyState !== WebSocket.OPEN) return;
    for (const payload of this.pending.splice(0)) this.socket.send(payload);
  }

  private scheduleReconnect(): void {
    if (this.reconnectTimer) return;
    this.reconnectTimer = setTimeout(() => {
      this.reconnectTimer = null;
      this.connect();
    }, this.reconnectDelay);
    this.reconnectDelay = Math.min(this.reconnectDelay * 2, 10_000);
  }
}

