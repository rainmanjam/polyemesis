# Light mode, and a card that has grown too many things

**Status: PLANNED for v0.3.0.** Researched 2026-08-04 against real product
references; nothing built. The references are recorded with their links so
picking this up does not mean re-doing the search.

**Recommendation: light mode on the PUBLIC PLAYER first, and leave the operator
console dark-only** until that palette has been built and looked at.

---

## The decision this changes, and why it is not simply a gap

`ui/src/index.css` states a position, and it is a considered one rather than an
omission:

> Design intent: a broadcast console, not a SaaS dashboard.
> - Surfaces are near-black and low-contrast so the room can be dark.
> - Exactly one desaturated accent carries interaction (slate blue).
> - Saturated colour is reserved for SIGNAL ONLY:
>     green = live / healthy · amber = reconnecting · red = down / clipping ·
>     cyan = armed but idle
>   **If everything is grey except the thing that is wrong, then the thing that
>   is wrong is readable from across the room.**
> - Dark is the only theme in v1. There is deliberately no light-mode block.

Adding light mode is therefore an argument with a stated design decision, not a
missing feature. It should win that argument on its merits or not happen.

**Where the argument is strong:** `PublicPlayer.tsx` is seen by an *audience*,
not an operator. They are not in a control room, they did not choose the
product, and a near-black page in a bright environment is a worse default for
them than for the person running the show.

**Where it is weak:** the console. The dark-room rationale is real, operators
self-select, and the signal palette was tuned for those surfaces.

## The hard part is the signal colours, not the toggle

**This palette cannot be inverted.** The "readable from across the room"
property is a function of dark surfaces: amber that screams on `#0b0d11` is
nearly invisible on white, and the green that reads as *live* washes out.

So the work is **two palettes derived independently against the same
perceptual intent**, not one set of `dark:` variants. Concretely, that means
choosing a target contrast ratio for "a failing destination is readable at a
glance from two metres" and solving for it in each theme.

Done casually, the result is a light mode where a failing destination looks
like a slightly warm label — which is worse than no light mode, because the
whole product promise is that trouble is visible before anyone reads a word.

## References

Searched on Mobbin, 2026-08-04. Web platform.

| Product | What to take | Link |
|---|---|---|
| **Cal.com** | **The model to copy.** Two independent theme settings — one for the logged-in dashboard, one for the *public* booking pages. polyemesis has exactly that split, and it is the reason this document recommends starting with the player | [screen](https://mobbin.com/screens/f561880a-1845-4179-904d-3c3aa8941090) |
| **GitHub** | Separates *when to switch* from *what each looks like*: a "sync with system" mode plus independent day-theme and night-theme pickers. The right shape if the console ever gains a light option, because it does not force one theme to be a compromise | [screen](https://mobbin.com/screens/4b8ade9d-a11e-46de-8734-53380fd824fc) |
| **Better Stack** | Closest peer — dense operational telemetry, dark by default. Light / Dark / System as three preview cards under "Look & Feel" | [screen](https://mobbin.com/screens/47f13a6b-5547-4aa6-bb4d-b8aa640e35d8) · [telemetry view](https://mobbin.com/screens/671cbbaa-6bd5-4e40-8a31-4e79c545be0a) |
| **Grok** | The cheapest thing that works: Light / Dark / System as a segmented row inside a settings modal | [screen](https://mobbin.com/screens/e5aafdde-2235-443b-bcd1-5834f497d1ef) |
| **Revolut Business** | Mode and *background* as separate choices. Recorded as a road not to take — decoration polyemesis has no use for | [screen](https://mobbin.com/screens/d6ff3090-344e-4121-965a-3530f2ebe12b) |
| **Discord** | A dark operator surface under real density, for comparison against ours | [screen](https://mobbin.com/screens/8411c1ef-51b5-4436-9d48-92dd89d62153) |

## Proposed scope for v0.3.0

1. **Derive the light signal palette.** Same four signal roles, re-solved for
   light surfaces against a stated contrast target. This is the whole risk of
   the project and should be done first, on paper, before any component
   changes.
2. **Apply it to `PublicPlayer` only.** One page, no operator surfaces.
3. **A per-surface setting**, Cal.com's split: the public player's theme is a
   setting; the console is not yet.
4. **Look at it.** In a bright room and a dark one, on a real stream, with a
   destination failing. The palette is only correct if trouble still reads from
   across the room.

**Out of scope, deliberately:** a console light theme. It becomes a reasonable
next step once the light signal palette exists and has been used, and an
unreasonable one before that.

## A separate observation, same area

`DestinationCard` has accreted quickly. It now carries the destination's state,
its warnings, its process error, the backup feed's state, the backup's error,
the scheduled-broadcast link, and the rendition label — seven things competing
for one card, each added by a change that was individually small and reasonable.

Nobody has looked at the result as a whole. **A hierarchy pass is worth doing
before an eighth thing is added**, and it is cheap: the question is only which
of those seven an operator needs at a glance and which can wait for a click.

## See also

- `ui/src/index.css` — the palette and the reasoning, in one place
- [UNREACHABLE-KNOBS.md](UNREACHABLE-KNOBS.md) — the sibling survey, for
  settings rather than surfaces
