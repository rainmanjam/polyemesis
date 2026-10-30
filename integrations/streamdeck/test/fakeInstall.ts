import type { Transport, TransportRequest, TransportResponse } from "../src/api.js";
import { Runtime, type Surface, type SurfaceKind } from "../src/runtime.js";

// The fake install, fake keys and hand-fired timers the track-level tests share.
// The same shapes as the helpers at the top of runtime.test.ts.

export type Route = (req: TransportRequest) => TransportResponse | Error;

/** A fake install. Routes are keyed "METHOD /api/v1/path"; anything unrouted is a 404. */
export function install(routes: Record<string, Route>) {
  const sent: TransportRequest[] = [];
  const transport: Transport = {
    async send(req) {
      sent.push(req);
      const route = routes[`${req.method} ${req.url.pathname}`];
      const out = route ? route(req) : { status: 404, body: '{"error":"no route"}' };
      if (out instanceof Error) throw out;
      return out;
    },
  };
  return { transport, sent, writes: () => sent.filter((r) => r.method === "POST") };
}

export const json = (status: number, body: unknown): Route => () => ({ status, body: JSON.stringify(body) });
export const ADMIN = json(200, { name: "Stream Deck", prefix: "pmk", scope: "admin", adminScope: true });
export const READ = json(200, { name: "Stream Deck", prefix: "pmk", scope: "read", adminScope: false });
export const PAIRED = { url: "https://studio.example", token: "t" };

export type Feedback = { title: string; value: string; indicator: number };

export function surface(id: string, kind: SurfaceKind, settings: Surface["settings"] = {}) {
  const rec = {
    states: [] as number[],
    titles: [] as string[],
    alerts: 0,
    oks: 0,
    feedback: [] as Feedback[],
  };
  const s: Surface & typeof rec = Object.assign(rec, {
    id,
    kind,
    settings,
    async setState(n: number) {
      rec.states.push(n);
    },
    async setTitle(title: string) {
      rec.titles.push(title);
    },
    async showAlert() {
      rec.alerts++;
    },
    async showOk() {
      rec.oks++;
    },
    async setFeedback(p: Feedback) {
      rec.feedback.push(p);
    },
  });
  return s;
}

/** Timers the test fires by hand, by their delay or all at once. */
export function manualScheduler() {
  const timers: { fn: () => void; ms: number; cancelled: boolean; ran: boolean }[] = [];
  const live = () => timers.filter((t) => !t.cancelled && !t.ran);
  return {
    schedule: (fn: () => void, ms: number) => {
      const t = { fn, ms, cancelled: false, ran: false };
      timers.push(t);
      return () => {
        t.cancelled = true;
      };
    },
    /** Fires every pending timer with this delay, once. */
    fire(ms: number) {
      for (const t of live().filter((x) => x.ms === ms)) (t.ran = true), t.fn();
    },
    fireAll() {
      for (const t of live()) (t.ran = true), t.fn();
    },
    pending: (ms?: number) => live().filter((t) => ms === undefined || t.ms === ms).length,
  };
}

export function runtimeFor(routes: Record<string, Route>) {
  const net = install(routes);
  const sched = manualScheduler();
  const rt = new Runtime("en", () => net.transport, sched.schedule);
  return { rt, net, sched };
}

/** Lets the promise chains a timer callback started run to the end. */
export async function settle(): Promise<void> {
  for (let i = 0; i < 20; i++) await Promise.resolve();
  await new Promise((r) => setImmediate(r));
}
