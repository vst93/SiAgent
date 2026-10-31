import { createInterface } from "node:readline";

// Every bundled channel worker speaks the same versioned NDJSON protocol on
// stdin/stdout: one JSON object per line, `v: 1`, at most 256 KiB per frame.
// The framing, byte bounding and shutdown rules live here so a worker only has
// to translate between its vendor SDK and platform messages. stdout carries the
// protocol alone: a worker must never print logs there, and stderr is discarded
// by the host, so credentials cannot leak through output.

export type Output = (value: object) => void;
export type Close = () => void;

export interface Session {
  /** Writes one protocol frame. Silently drops after close. */
  output: Output;
  /** Reports a connection state transition using the platform's fixed set. */
  state: (state: string) => void;
  /** Idempotent shutdown: closes the SDK connection and releases stdin. */
  close: Close;
  /** Registers cleanup that runs once when the session closes. */
  onClose: (fn: () => void) => void;
  /** Marker carried on every frame; workers echo it back unchanged. */
  closed: () => boolean;
}

export type FrameHandler = (frame: any, session: Session) => void;

const maxFrameBytes = 262144;
// Stop writing when the consumer is not draining; the host kills the process
// group, and a growing buffer would only delay that with memory pressure.
const maxBufferedBytes = 1048576;

/** Serves one worker session over stdin/stdout until stdin closes. */
export function serve(handle: FrameHandler): void {
  const cleanups: (() => void)[] = [];
  let closed = false;
  const session: Session = {
    output(value) {
      if (closed) return;
      if (process.stdout.writableLength > maxBufferedBytes) {
        session.close();
        return;
      }
      process.stdout.write(JSON.stringify({ v: 1, ...value }) + "\n");
    },
    state(state) {
      session.output({ type: "status", state });
    },
    close() {
      if (closed) return;
      closed = true;
      for (const cleanup of cleanups) {
        try {
          cleanup();
        } catch {
          // A failing cleanup must not prevent the remaining ones or the exit.
        }
      }
      process.stdin.destroy();
    },
    onClose(fn) {
      cleanups.push(fn);
    },
    closed: () => closed,
  };
  const lines = createInterface({ input: process.stdin, crlfDelay: Infinity });
  // Bound partial frames as well as complete lines.
  let bytes = 0;
  process.stdin.on("data", (chunk: Buffer) => {
    for (const byte of chunk) {
      bytes = byte === 10 ? 0 : bytes + 1;
      if (bytes > maxFrameBytes) {
        session.close();
        break;
      }
    }
  });
  lines.on("close", () => session.close());
  process.on("SIGTERM", () => session.close());
  process.on("SIGINT", () => session.close());
  lines.on("line", (line) => {
    if (session.closed()) return;
    let frame: any;
    try {
      if (Buffer.byteLength(line) > maxFrameBytes) {
        session.close();
        return;
      }
      frame = JSON.parse(line);
      if (frame?.v !== 1) {
        session.close();
        return;
      }
    } catch {
      session.close();
      return;
    }
    try {
      handle(frame, session);
    } catch {
      session.close();
    }
  });
}
