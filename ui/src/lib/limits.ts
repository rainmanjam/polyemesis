/** Input bounds, mirroring the Go validators.
 *
 *  These exist so a numeric field can refuse an out-of-range value at the
 *  keystroke rather than accept it and report the problem a round trip later.
 *  The server is still the authority — `IngestSettings.problems()` and
 *  `Rendition.Validate` decide what is actually storable — but a form that
 *  accepts 70000 into a port field has made the operator do the checking.
 *
 *  Kept in ONE file rather than typed per field, because the failure mode of
 *  scattering them is silent: a form that permits what the server refuses
 *  produces a save button that does nothing and an error nobody connects to the
 *  field they typed in. When a Go bound changes, this file is the single place
 *  that has to follow.
 *
 *  Source of truth, for anyone updating these:
 *    port            internal/db/settings.go, IngestSettings.problems
 *    srtLatencyMs    same
 *    srtPassphrase   same (SRT's own 10..79 constraint)
 *    rendition*      internal/db/renditions.go, the Min/Max consts
 *    audioBitrate    internal/db/destinations.go, Validate
 */
export interface Bound {
  min: number;
  max: number;
}

export const LIMITS = {
  /** TCP/UDP port. 1 rather than 0: to the kernel :0 means "any free port",
   *  which binds something random and reports success — see the manager's
   *  guard for the same reason. */
  port: { min: 1, max: 65535 } as Bound,

  /** SRT receive buffer. Below ~20ms the protocol cannot recover anything;
   *  above 8s the glass-to-glass delay stops being live. */
  srtLatencyMs: { min: 20, max: 8000 } as Bound,

  /** SRT's own constraint on passphrase length, not ours. */
  srtPassphrase: { min: 10, max: 79 } as Bound,

  renditionDimension: { min: 128, max: 7680 } as Bound,
  renditionFPS: { min: 1, max: 240 } as Bound,
  renditionBitrateKbps: { min: 100, max: 100_000 } as Bound,
  renditionGOPSeconds: { min: 1, max: 10 } as Bound,

  audioBitrateKbps: { min: 32, max: 512 } as Bound,

  /** Recording retention. 10s segments are already impractically short; a day
   *  is the longest single file worth writing. */
  recordingSegmentSeconds: { min: 10, max: 86_400 } as Bound,

  /** The in-memory chat ring. Two orders of magnitude below the stored
   *  keepMessages floor, because this one is allocated in full at startup:
   *  the ceiling is memory reserved, not a limit on what may accumulate. */
  chatHistoryMessages: { min: 1, max: 50_000 } as Bound,

  /** Alert delivery attempts, first try included. Ten is already several
   *  minutes of chasing one dead endpoint, because the backoff behind it
   *  climbs to a 30s ceiling. */
  alertRetryAttempts: { min: 1, max: 10 } as Bound,
} as const;
