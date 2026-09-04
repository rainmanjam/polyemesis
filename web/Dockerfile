# syntax=docker/dockerfile:1

# --- build ------------------------------------------------------------------
FROM node:24-alpine AS build
WORKDIR /app

# Dependencies first so a content edit does not invalidate the install layer.
COPY package.json package-lock.json ./
# --ignore-scripts: a build must not execute arbitrary postinstall hooks from the
# dependency tree. ci.yml already installs this way in every job that touches
# web/ and ui/; this image was the one place that did not, so the hardening
# stopped at the edge of the container. Sonar's docker:S6505 caught the gap.
RUN npm ci --ignore-scripts --no-audit --no-fund

COPY . .
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
FROM nginxinc/nginx-unprivileged:1.31-alpine AS runtime

# Cloud Run sends SIGTERM and expects the process to go. nginx's default is a
# graceful stop on SIGQUIT and a FAST stop on SIGTERM, which is what we want
# here — there are no long-lived requests to drain on a static site.
STOPSIGNAL SIGTERM

COPY nginx.conf /etc/nginx/templates/default.conf.template
# Not a template: it has no ${PORT} to expand, and the entrypoint only
# substitutes files under /etc/nginx/templates.
COPY nginx-security-headers.conf /etc/nginx/snippets/security-headers.conf
COPY --from=build /app/dist /usr/share/nginx/html

# Cloud Run injects PORT and it is not always 8080. nginx's image expands
# templates in /etc/nginx/templates at start-up, so the listen directive picks
# it up rather than being baked in.
ENV PORT=8080
EXPOSE 8080

CMD ["nginx", "-g", "daemon off;"]
