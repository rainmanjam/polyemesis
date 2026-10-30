import { useEffect, useRef, useState } from "react";

import { autoApi } from "./autoApi";
import type { DestinationId } from "./types";

/* What still points at a destination, for its delete confirmation (A18).
 *
 * Schedules name destinations by id and nothing removes the id when the row
 * goes: a schedule that started "YouTube main" every Sunday starts nothing
 * after the delete, quietly. So the dialog says how many there are before the
 * operator confirms, the way the source and rendition deletes already count
 * what they take with them.
 *
 * Fetched when the dialog opens rather than kept in the page: it is one small
 * list, read once per delete, and a stale count is the one thing this must not
 * show. */

export type DestinationUsage =
  | { status: "idle" | "loading" | "failed" }
  | { status: "ready"; schedules: number };

/** Counts the schedules that name `id`. Only the ids matter here. */
export function schedulesUsing(schedules: readonly { destinationIds: number[] | null }[], id: number): number {
  return schedules.filter((s) => (s.destinationIds ?? []).includes(id)).length;
}

export function useDestinationUsage(id: DestinationId | null): DestinationUsage {
  const [usage, setUsage] = useState<DestinationUsage>({ status: "idle" });
  const seq = useRef(0);
  useEffect(() => {
    const mine = ++seq.current;
    if (id === null) return;
    // Loading is set in the same tick the request goes out: a dialog that
    // opened on the previous target's count would be the stale read this
    // exists to prevent.
    queueMicrotask(() => mine === seq.current && setUsage({ status: "loading" }));
    autoApi.get<{ destinationIds: number[] | null }[]>("/schedules").then(
      (list) => mine === seq.current && setUsage({ status: "ready", schedules: schedulesUsing(list ?? [], id) }),
      () => mine === seq.current && setUsage({ status: "failed" }),
    );
  }, [id]);
  return id === null ? { status: "idle" } : usage;
}
