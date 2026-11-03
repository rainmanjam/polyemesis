# Interactive homepage concept

Open `/signal-path-sample.html` through a local HTTP server (`web/public` is the document root), for example:

    python3 -m http.server 4326 --bind 127.0.0.1 --directory web/public

This is a standalone prototype. It is not linked from the Astro site.

The preferred preview at `/signal-path-sample.html` uses:

- `controller.js` — the rAF timeline, chapter buttons, play/pause/replay, aria-live narration, live card meters, the YouTube music switch, pointer tilt, and the fallbacks.
- `routes.js` — wire geometry shared by the WebGL layer and the SVG fallback (four lanes per connection: video, mic, music, game).
- `scene.js` — the layer behind the cards: `Line2` wires, instanced packets with additive trails, and a selective `UnrealBloomPass` on the packets only.
- `faders.js` — the polyemesis mark (from `favicon.svg`) as five 3D faders inside the router card. Channels are spare, Mic, Music, Game, spare. A hovered or focused destination, or the current chapter, sets the mix.

Colours: track identity is mic `#45b5d0`, game `#9b8cff`, music `#e879f9`. Green, amber and orange-red are status only and unused here. Blue is interaction only.

Fallbacks: if the wire layer cannot start or loses its WebGL context, the SVG wires and ports show (`three-ready` is removed). If the faders cannot start or lose context, the static SVG mark shows (`core-ready` is removed). The two layers fail independently.

Motion: no GSAP, no ScrollTrigger and no scroll-driven reveals. Rendering pauses when the demo is offscreen or the document is hidden. With `prefers-reduced-motion` the diagram is a static completed frame (packets frozen on the wires, meters static, no tilt). Pointer tilt is 2.5 degrees at most, on the faders only, and is off for touch-only devices.

Video still: `video-still.webp` is used when present, with `video-frame.svg` as the fallback.

The alternate broadcast experience is preserved at `/signal-path-experience.html`. Its entry point is `experience.js`; it loads the local audio bank, shared animated program frame, and signal field.

The three scenarios demonstrate independent destination mixes, retained recording stems, and configured source failover. This is a browser simulation, not a connection to a running Polyemesis instance. No claim of measured loudness or live telemetry is made.

Audio starts only when Listen or Hear the difference is clicked on the alternate page. Original assets: generated narration with macOS Samantha; synthesized music and game sounds; procedural night-drive illustration. No third-party video, music, or illustrations were copied.

Visual research:
- https://www.mux.com/ — exploring an actual media asset and its outputs.
- https://learningmusic.ableton.com/ — learning through immediately audible combinations.
- https://rive.app/blog/intercom-s-product-animation-evolution-embracing-rive — product behavior communicated through responsive animation.

Three.js 0.187.0 and its `examples/jsm` addons are bundled locally under `vendor` with the MIT license (see `vendor/README.md`). No CDN is needed during playback.
