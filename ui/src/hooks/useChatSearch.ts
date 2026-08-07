import { useCallback, useEffect, useMemo, useRef, useState } from "react";

import { api } from "@/lib/api";
import type { ChatMessage, ChatPlatform } from "@/lib/types";

/* Searching the retained scrollback.
 *
 * Server-side, always. The pane holds one session's messages and the whole
 * point of the search box is to reach the comment that scrolled away — a
 * client-side filter over what is already on screen answers a question the
 * operator did not ask.
 *
 * Two things this hook is careful about, both of which make the difference
 * between a search box and a frustrating one:
 *
 *   - It debounces, so typing "moderator" is one request rather than nine.
 *   - It drops stale responses. Requests can land out of order, and a slow
 *     reply for "mod" arriving after the fast one for "moderator" would leave
 *     the box saying one thing and the list showing another. */

/** Long enough to swallow a burst of typing, short enough that the results feel
 *  attached to the keystrokes. */
const DEBOUNCE_MS = 250;

/** Below this a search matches so much that the result set is noise. */
const MIN_QUERY = 2;

export interface ChatSearchState {
  query: string;
  setQuery: (q: string) => void;
  /** True once the query is long enough to have been searched for. Drives
   *  whether the UI shows results INSTEAD of the live timeline. */
  active: boolean;
  results: ChatMessage[];
  loading: boolean;
  error: string;
  /** More matches exist beyond the limit. */
  truncated: boolean;
  /** Why the history only goes back so far. Render it, including on an empty
   *  result — "nothing found" must not be read as "never said". */
  note: string;
  clear: () => void;
}

export function useChatSearch(platform?: ChatPlatform): ChatSearchState {
  const [query, setQuery] = useState("");
  const [results, setResults] = useState<ChatMessage[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const [truncated, setTruncated] = useState(false);
  const [note, setNote] = useState("");

  const trimmed = query.trim();
  const active = trimmed.length >= MIN_QUERY;

  // Identifies the in-flight request. Anything that comes back with a stale
  // token is discarded rather than rendered.
  const seq = useRef(0);

  useEffect(() => {
    if (!active) {
      seq.current++; // cancel whatever is in flight
      setResults([]);
      setLoading(false);
      setError("");
      setTruncated(false);
      return;
    }

    const token = ++seq.current;
    setLoading(true);
    const timer = setTimeout(() => {
      api
        .chatSearch({ q: trimmed, platform, limit: 200 })
        .then((r) => {
          if (seq.current !== token) return;
          setResults(r.messages ?? []);
          setTruncated(Boolean(r.truncated));
          setNote(r.retentionNote ?? "");
          setError("");
        })
        .catch((err: unknown) => {
          if (seq.current !== token) return;
          setResults([]);
          setError(err instanceof Error ? err.message : "Search failed.");
        })
        .finally(() => {
          if (seq.current === token) setLoading(false);
        });
    }, DEBOUNCE_MS);

    return () => clearTimeout(timer);
  }, [trimmed, active, platform]);

  const clear = useCallback(() => setQuery(""), []);

  return useMemo(
    () => ({ query, setQuery, active, results, loading, error, truncated, note, clear }),
    [query, active, results, loading, error, truncated, note, clear],
  );
}
