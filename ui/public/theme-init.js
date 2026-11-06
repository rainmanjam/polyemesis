/* Sets <html data-theme> before the first paint, so a light-mode operator never
 * sees the console flash dark.
 *
 * AN EXTERNAL FILE ON PURPOSE. The server's Content-Security-Policy has no
 * 'unsafe-inline' for scripts (internal/api/security.go), so an inline <script>
 * would be blocked, and loosening the policy for a flash is the wrong trade.
 * This is loaded synchronously from <head> -- no defer, no module -- so it
 * runs before the body is parsed.
 *
 * THE SAME RULE AS src/lib/theme.ts, in ES5 because nothing here is compiled:
 *   - not on /watch or the /overlay pages (they are never themed);
 *   - the stored choice "light" or "dark" wins;
 *   - anything else (nothing stored, "system", garbage, storage that throws)
 *     follows prefers-color-scheme.
 * src/lib/themeMode.test.ts runs this file against that module over the same
 * cases, so the two cannot drift apart unnoticed. */
(function () {
  var path = location.pathname;
  if (path === "/watch" || path.indexOf("/watch/") === 0 || /^\/overlay(\/(dock|poll))?(\/k\/[^/]+)?\/*$/.test(path)) return;

  var choice = "system";
  try {
    var stored = localStorage.getItem("polyemesis.theme");
    if (stored === "light" || stored === "dark") choice = stored;
  // eslint-disable-next-line no-unused-vars -- ES5: no optional catch binding
  } catch (_e) {
    /* storage unavailable: follow the system */
  }

  var light;
  if (choice === "system") {
    try {
      light = matchMedia("(prefers-color-scheme: light)").matches;
    // eslint-disable-next-line no-unused-vars -- ES5: no optional catch binding
    } catch (_e) {
      light = false;
    }
  } else {
    light = choice === "light";
  }

  var theme = light ? "light" : "dark";
  var root = document.documentElement;
  root.setAttribute("data-theme", theme);

  var scheme = document.querySelector('meta[name="color-scheme"]');
  if (scheme) scheme.setAttribute("content", theme);
  var chrome = document.querySelector('meta[name="theme-color"]');
  if (chrome) chrome.setAttribute("content", light ? "#f1f3f7" : "#0f1115");
})();
