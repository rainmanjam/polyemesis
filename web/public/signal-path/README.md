# Interactive homepage concept

Open `/signal-path-sample.html` through a local HTTP server (`web/public` is the document root).

The preferred preview at `/signal-path-sample.html` uses `controller.js` and `scene.js`: a Three.js routing board, colored packets, destination mixes, and a YouTube music switch.

The alternate broadcast experience is preserved at `/signal-path-experience.html`. Its entry point is `experience.js`; it loads the local audio bank, shared animated program frame, and signal field described below.

The three scenarios demonstrate independent destination mixes, retained recording stems, and configured source failover. This is a browser simulation, not a connection to a running Polyemesis instance. The waveform and audible mix selections share the same track masks. No claim of measured loudness or live telemetry is made.

Audio starts only when Listen or Hear the difference is clicked. Original assets: generated narration with macOS Samantha; synthesized music and game sounds; procedural night-drive illustration. No third-party video, music, or illustrations were copied. Reduced motion is supported. Rendering pauses when the demo is offscreen or the document is hidden.

Visual research:
- https://www.mux.com/ — exploring an actual media asset and its outputs.
- https://learningmusic.ableton.com/ — learning through immediately audible combinations.
- https://rive.app/blog/intercom-s-product-animation-evolution-embracing-rive — product behavior communicated through responsive animation.

Three.js 0.187.0 is bundled locally under `vendor` with its MIT license. No CDN is needed during playback.
