// @ts-check
/* Rewrites the links inside docs/*.md so they work on polyemesis.com.
 *
 * Every one of these documents was written to be read on GitHub, where
 * `](AUDIO-ROUTING.md)` resolves against the directory the reader is standing
 * in. Served from /docs/obs, that same link points at /docs/AUDIO-ROUTING.md,
 * which does not exist. There are 73 of them across the published set, plus 34
 * more pointing outside docs/ entirely.
 *
 * A LINK THAT SILENTLY 404s IS WORSE THAN THE GITHUB LINK IT REPLACED, so this
 * resolves rather than pattern-matches, and it fails the build on a target that
 * is not there. Three destinations, in order:
 *
 *   1. A published document        -> /docs/<slug>, anchor preserved.
 *   2. An unpublished document, or -> the file on github.com. Not dropped and
 *      any other path in the repo     not silently broken: the reader still
 *                                     gets the thing, it is just not a page
 *                                     here. This is the honest half of an
 *                                     allowlist -- excluding a file from the
 *                                     site does not delete it from the world.
 *   3. Absolute, mailto, or a bare -> untouched.
 *      fragment
 *
 * The existence check is the part worth keeping. Rewriting to github.com is the
 * fallback for "we do not publish that", and without a check it silently becomes
 * the fallback for "that file was renamed two releases ago" as well -- a 404 on
 * somebody else's domain, which nothing here would ever notice. The rewritten
 * INTERNAL links are checked from the other side, by check-build.mjs walking the
 * built HTML; between the two, every link on a docs page is verified.
 */
import { existsSync } from "node:fs";
import { GITHUB_BLOB, GITHUB_TREE, BY_FILE, hrefOf } from "../data/docs.mjs";

/** Repo root, from web/src/lib/. */
const REPO = new URL("../../../", import.meta.url).pathname;

/** POSIX path normalisation. `docs/../deploy/nginx.conf.example` -> `deploy/nginx.conf.example`.
 * @param {string} p
 * @returns {string} */
function normalise(p) {
  /** @type {string[]} */
  const out = [];
  for (const part of p.split("/")) {
    if (part === "" || part === ".") continue;
    if (part === "..") out.pop();
    else out.push(part);
  }
  return out.join("/");
}

/**
 * @param {string} url   The link as written in the markdown.
 * @param {string} from  The document being rendered, e.g. "OBS.md".
 * @returns {string}     The link as it should appear on the site.
 */
export function rewriteDocLink(url, from) {
  // Absolute, protocol-relative, mailto, and same-page fragments are already right.
  if (/^([a-z][a-z0-9+.-]*:|\/\/|\/|#)/i.test(url)) return url;

  const hashAt = url.indexOf("#");
  const target = hashAt < 0 ? url : url.slice(0, hashAt);
  const hash = hashAt < 0 ? "" : url.slice(hashAt);
  if (!target) return url;

  // A sibling document, which is the case that matters: 73 of them.
  const sibling = BY_FILE.get(target);
  if (sibling) return hrefOf(sibling) + hash;

  // Everything else resolves against docs/, where every published file lives,
  // and lands on github.com. A trailing slash means a directory, and GitHub
  // serves those under /tree/ rather than /blob/.
  const isDir = target.endsWith("/");
  const repoPath = normalise(`docs/${target}`);
  if (!repoPath) return url;

  if (!existsSync(REPO + repoPath)) {
    throw new Error(
      `docs/${from} links to "${url}", which resolves to ${repoPath} and is not in the repo.\n` +
        `  It would have been rewritten to a github.com URL that 404s -- worse than the link it ` +
        `replaced, and invisible to this site's own link check, because the host is not ours.`,
    );
  }
  return `${isDir ? GITHUB_TREE : GITHUB_BLOB}/${repoPath}${hash}`;
}

/* A Sätteri mdast plugin rather than a remark one. Astro 7 renders markdown
 * through Sätteri by default; `remarkPlugins` still work, but only after
 * installing @astrojs/markdown-remark and switching the whole pipeline back to
 * unified, which is a large change to make for three small visitors.
 *
 * `link` is an inline link, `definition` backs a reference-style one, and
 * `image` covers a relative screenshot. There are no images in these documents
 * today; that visitor is here because adding one is the obvious next edit and it
 * would break in exactly the same way, silently.
 */

/** Every mdast node that carries a `url`: Link, Image, Definition.
 * @typedef {Extract<import("satteri").MdastNode, { url: string }>} UrlNode */

/** @type {import("satteri").MdastPluginDefinition} */
export const mdastDocLinks = {
  name: "polyemesis:doc-links",
  link: (node, ctx) => rewrite(node, ctx),
  definition: (node, ctx) => rewrite(node, ctx),
  image: (node, ctx) => rewrite(node, ctx),
};

/** @param {Readonly<UrlNode>} node @param {import("satteri").MdastVisitorContext} ctx */
function rewrite(node, ctx) {
  if (typeof node.url !== "string") return;
  const from = (ctx.fileURL?.pathname ?? "").split("/").pop() ?? "";
  const next = rewriteDocLink(node.url, from);
  if (next !== node.url) ctx.setProperty(node, "url", next);
}
