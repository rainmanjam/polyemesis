# syntax=docker/dockerfile:1

# Both FROMs are tag@sha256 (multi-arch index digests, checked 2026-11-02), like
# the root Dockerfiles: one commit always builds from the same bytes. Upstream
# fixes arrive as dependabot digest bumps (.github/dependabot.yml, docker,
# directory "/web"). Keep the tag when bumping by hand -- dependabot reads it.
# internal/testenv/dockerfile_digest_pin_test.go fails a FROM without a digest.
#
# BUILD FROM THE REPOSITORY ROOT:
#
#   docker build -f web/Dockerfile -t polyemesis-site .
#
# The build context is the repo root, not web/. The site is not self-contained:
# the docs pages are docs/*.md (web/src/content.config.ts, base "../docs"),
# check-build.mjs reads docs/ and internal/db/platforms.go, and
# web/src/lib/mdast-doc-links.mjs fails the build when a doc links to a repo
# path that does not exist -- and the docs link into deploy/, internal/,
# integrations/, scripts/ and the root files copied below. With web/ as the
# context the build died on `ENOENT: scandir '/docs/'`.
#
# The context is cut down by web/Dockerfile.dockerignore (BuildKit reads
# <Dockerfile>.dockerignore in preference to the root .dockerignore, which
# belongs to the release images and excludes docs/). It is an allow-list: only
# the paths copied below, minus node_modules, build output, testdata and
# anything secret-shaped. ui/ is deliberately absent -- see
# docs/DESIGN-SYSTEM.md on why the design tokens are a checked-in copy.

# --- build ------------------------------------------------------------------
FROM node:24-alpine@sha256:a34ab6d7f6f4813f4d0c5469d74685604b962fbdbf5793a7bc7b4998ecb867c8 AS build
# The repo layout is reproduced under /repo, so web/'s relative paths (../docs,
# ../../internal/...) resolve exactly as they do in a checkout.
WORKDIR /repo/web

# Dependencies first so a content edit does not invalidate the install layer.
COPY web/package.json web/package-lock.json ./
# --ignore-scripts: a build must not execute arbitrary postinstall hooks from the
# dependency tree. ci.yml already installs this way in every job that touches
# web/ and ui/; this image was the one place that did not, so the hardening
# stopped at the edge of the container. Sonar's docker:S6505 caught the gap.
RUN npm ci --ignore-scripts --no-audit --no-fund

# What the build reads outside web/. Whole directories rather than the files
# linked today: a doc may link to any file in them, and a narrower list would
# break this build the next time someone adds a link.
COPY docs/ /repo/docs/
COPY deploy/ /repo/deploy/
COPY internal/ /repo/internal/
COPY integrations/ /repo/integrations/
COPY scripts/ /repo/scripts/
COPY README.md CHANGELOG.md SECURITY.md CONTRIBUTING.md CODE_OF_CONDUCT.md \
     LICENSE config.example.yaml docker-compose.yml /repo/
COPY web/ /repo/web/
RUN npm run build

# --- serve ------------------------------------------------------------------
# nginx rather than a Node runtime. The output is static files; keeping a
# JavaScript runtime alive to hand them over would add a cold start, a process
# to supervise and a much larger patch surface, for no capability.
# nginxinc/nginx-unprivileged, not the stock nginx image. The stock one expects
# root at start-up: its entrypoint expands /etc/nginx/templates into
# /etc/nginx/conf.d and creates /var/cache/nginx, and adding `USER nginx` to it
# breaks both with permission errors that only surface at run time. This variant
# is built to run rootless and defaults to 8080, which is also what Cloud Run
# wants.
FROM nginxinc/nginx-unprivileged:1.31-alpine@sha256:b5047b9b76845eac124358373c6852377c52e4b05834a47647e955dd91cb8f1e AS runtime

# Cloud Run sends SIGTERM and expects the process to go. nginx's default is a
# graceful stop on SIGQUIT and a FAST stop on SIGTERM, which is what we want
# here — there are no long-lived requests to drain on a static site.
STOPSIGNAL SIGTERM

COPY web/nginx.conf /etc/nginx/templates/default.conf.template
# Not a template: it has no ${PORT} to expand, and the entrypoint only
# substitutes files under /etc/nginx/templates.
COPY web/nginx-security-headers.conf /etc/nginx/snippets/security-headers.conf
COPY --from=build /repo/web/dist /usr/share/nginx/html

# Cloud Run injects PORT and it is not always 8080. nginx's image expands
# templates in /etc/nginx/templates at start-up, so the listen directive picks
# it up rather than being baked in.
ENV PORT=8080
EXPOSE 8080

CMD ["nginx", "-g", "daemon off;"]
