# Deploying web/ to Google Cloud Run

Written 2026-08-13, against `web/` as it stands: Astro 7 with
`output: "static"`, building to **6 HTML pages, 24 files, 2.9 MB**.

## The concern, once

Cloud Run runs a container that answers HTTP requests. This site is 24 static
files. Putting them behind Cloud Run means running a web server process to hand
out files a CDN would serve faster, closer to the user, and for less — and Cloud
Run has no CDN of its own, so a global audience either takes the latency or you
add an HTTPS load balancer in front at roughly **$18/month before any traffic**.

If the goal is "the site, on Google Cloud", **Firebase Hosting** is the same
company, purpose-built for exactly this, global CDN included, free at this size,
automatic certificates. It is three commands.

Cloud Run earns its place if you want one deployment surface for everything, or
you plan to add server-rendered routes later. Both are real reasons. What
follows is the working version.

**Not a reason:** hosting polyemesis itself. Cloud Run terminates HTTP only —
no RTMP on 1935, no SRT on UDP — so the server cannot live there regardless of
what the site does.

---

## Option A — Cloud Run (what you asked for)

### 1. The container

`web/Dockerfile`:

```dockerfile
# Build the static site.
FROM node:24-alpine AS build
WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci --ignore-scripts
COPY . .
RUN npm run build

# Serve it. nginx rather than a Node process: the site is static, and a
# runtime that can execute application code is a larger thing to keep patched
# than one that cannot.
FROM nginx:1.29-alpine
COPY --from=build /app/dist /usr/share/nginx/html
COPY nginx.conf /etc/nginx/templates/default.conf.template
# Cloud Run sets PORT and nginx must honour it -- the default of 80 will not be
# reached and the revision fails its health check with no useful message.
ENV PORT=8080
EXPOSE 8080
```

`web/nginx.conf` — note `${PORT}`, which the nginx image's entrypoint
substitutes because the file is under `/etc/nginx/templates`:

```nginx
server {
    listen       ${PORT};
    server_name  _;
    root         /usr/share/nginx/html;
    index        index.html;

    # Astro emits /about/index.html for /about. Without this, /about 404s.
    location / {
        try_files $uri $uri/index.html $uri.html =404;
    }

    # Hashed assets are immutable; HTML must not be, or a deploy is invisible
    # until caches expire.
    location /_astro/ {
        add_header Cache-Control "public, max-age=31536000, immutable";
    }
    location ~* \.html$ {
        add_header Cache-Control "public, max-age=0, must-revalidate";
    }

    gzip on;
    gzip_types text/css application/javascript image/svg+xml application/json;
}
```

### 2. First deploy, by hand

```bash
gcloud config set project zipper-picker
gcloud services enable run.googleapis.com artifactregistry.googleapis.com

gcloud artifacts repositories create web \
  --repository-format=docker --location=us-central1

# --source builds with Cloud Build; no local Docker needed.
gcloud run deploy polyemesis-web \
  --source web/ \
  --region us-central1 \
  --allow-unauthenticated \
  --min-instances 0 \
  --max-instances 4 \
  --cpu 1 --memory 512Mi \
  --port 8080
```

`--min-instances 0` means no idle cost and a cold start of roughly a second for
nginx. `--min-instances 1` removes the cold start and costs about **$13/month**
for the always-on instance.

### 3. Custom domain

```bash
gcloud beta run domain-mappings create \
  --service polyemesis-web --domain polyemesis.com --region us-central1
```

It prints DNS records to add. Certificates are managed for you. Domain mapping
is not available in every region — if `us-central1` refuses, either move the
service or put a load balancer in front (see below).

### 4. Deploying from GitHub Actions, without a service-account key

Workload Identity Federation. No JSON key in a secret, which is the part worth
getting right — a leaked service-account key is a standing credential, and this
repository has spent a fair amount of this month on exactly that class of
problem.

One-time setup:

```bash
PROJECT=zipper-picker
PROJNUM=$(gcloud projects describe $PROJECT --format='value(projectNumber)')

gcloud iam service-accounts create gh-deploy-web \
  --display-name="GitHub Actions - web deploy"

gcloud iam workload-identity-pools create github --location=global
gcloud iam workload-identity-pools providers create-oidc github \
  --location=global --workload-identity-pool=github \
  --issuer-uri="https://token.actions.githubusercontent.com" \
  --attribute-mapping="google.subject=assertion.sub,attribute.repository=assertion.repository" \
  --attribute-condition="assertion.repository=='rainmanjam/polyemesis'"

# Only this repository may impersonate the account.
gcloud iam service-accounts add-iam-policy-binding \
  gh-deploy-web@$PROJECT.iam.gserviceaccount.com \
  --role=roles/iam.workloadIdentityUser \
  --member="principalSet://iam.googleapis.com/projects/$PROJNUM/locations/global/workloadIdentityPools/github/attribute.repository/rainmanjam/polyemesis"

for R in roles/run.admin roles/artifactregistry.writer roles/cloudbuild.builds.editor roles/iam.serviceAccountUser; do
  gcloud projects add-iam-policy-binding $PROJECT \
    --member="serviceAccount:gh-deploy-web@$PROJECT.iam.gserviceaccount.com" --role=$R
done
```

`.github/workflows/cloud-run-web.yml`:

```yaml
name: deploy web to cloud run

on:
  push:
    branches: [main]
    paths: ["web/**", ".github/workflows/cloud-run-web.yml"]
  workflow_dispatch:

permissions:
  contents: read
  id-token: write   # required for WIF; without it auth fails with no clear reason

concurrency:
  group: cloud-run-web
  cancel-in-progress: true

jobs:
  deploy:
    runs-on: ubuntu-latest
    timeout-minutes: 20
    steps:
      - uses: actions/checkout@v5

      - uses: google-github-actions/auth@v3
        with:
          project_id: zipper-picker
          workload_identity_provider: projects/PROJECT_NUMBER/locations/global/workloadIdentityPools/github/providers/github
          service_account: gh-deploy-web@zipper-picker.iam.gserviceaccount.com

      - uses: google-github-actions/setup-gcloud@v3

      - name: deploy
        run: |
          gcloud run deploy polyemesis-web \
            --source web/ \
            --region us-central1 \
            --allow-unauthenticated \
            --min-instances 0 --max-instances 4 \
            --port 8080 \
            --quiet
```

Substitute the real project number in `workload_identity_provider`; the string
is not secret and does not belong in a secret.

### What it costs

At this size, essentially nothing in request charges — Cloud Run's free tier
covers 2 million requests a month. The real numbers are the optional ones:
**$13/month** for `--min-instances 1` to kill cold starts, **~$18/month** for a
load balancer if you want Cloud CDN or a region without domain mapping.

---

## Option B — Firebase Hosting (the fit for a static site)

Same Google account, same billing, global CDN, free at this size.

```bash
npm i -g firebase-tools
firebase login
firebase init hosting     # public dir: dist, SPA: no
cd web && npm run build && firebase deploy --only hosting
```

`web/firebase.json`:

```json
{
  "hosting": {
    "public": "dist",
    "cleanUrls": true,
    "trailingSlash": false,
    "ignore": ["firebase.json", "**/.*", "**/node_modules/**"],
    "headers": [
      { "source": "/_astro/**",
        "headers": [{ "key": "Cache-Control", "value": "public, max-age=31536000, immutable" }] },
      { "source": "**/*.html",
        "headers": [{ "key": "Cache-Control", "value": "public, max-age=0, must-revalidate" }] }
    ]
  }
}
```

CI uses the same Workload Identity Federation, with
`FirebaseHostingAdmin` instead of the Cloud Run roles.

No container, no cold start, no load balancer, no `$PORT` handling, and the
cache headers that took a paragraph of nginx config are four lines of JSON.

---

## Option C — Cloud Storage + Cloud CDN

A bucket behind an HTTPS load balancer. Works, scales, and is what you would
choose if the site had to sit inside a VPC or share a load balancer with other
GCP services. Otherwise it is Firebase Hosting with more moving parts and an
$18/month floor for the load balancer.

---

## Cost, with the numbers checked

Every figure below was read from the vendor's own docs on 2026-08-13, not
recalled. A "page view" here is this site's HTML + CSS + JS, measured at
**81 KB**; screenshots push a first visit higher, and caching pushes repeat
visits to nearly nothing.

| | fits | monthly | the ceiling that ends "free" |
|---|---|---|---|
| **Cloudflare Pages** | a static site | **$0** | none on traffic — see below |
| **Firebase Hosting** | a static site | **$0** | 360 MB/day transfer, then $0.15/GB |
| Cloud Run | one deploy surface, or future SSR | $0–13 | 2M requests/mo, then per-request |
| Cloud Run + CDN | global, low latency | ~$18+ | the load balancer is a floor, not a ceiling |
| GCS + Cloud CDN | must live in a VPC | ~$18+ | same load-balancer floor |

### Cloudflare Pages — $0, and no traffic ceiling

From Cloudflare's own Functions pricing page:

> On both free and paid plans, requests to static assets are **free and
> unlimited**. A request is considered static when it does not invoke Functions.

`web/` is `output: "static"` with no Functions, so every request it serves is a
static one. There is no bandwidth meter to exceed.

What the free plan *does* limit, against what this site uses:

| limit (free) | this site |
|---|---|
| 500 builds/month | one per push to `web/` |
| 20,000 files per site | **24** |
| 25 MiB per file | largest is 576 KB |
| 100 custom domains | 1 |
| 1 concurrent build, 20-min timeout | builds in **0.5s** |

The only thing that would start a bill is adding Pages **Functions** — server-
side routes — which moves you onto Workers Paid at **$5/month minimum**. A
static Astro build never triggers it.

### Firebase Hosting — $0, with a real ceiling

I said "$0" for Firebase earlier without qualifying it, and that was too simple.
The free tier is **10 GB storage and 360 MB/day of transfer**, then $0.026/GB
stored and $0.15/GB transferred.

At 81 KB per page view that is roughly **4,500 first-time views a day** before
anything is owed, and more once caching is counted. Ample for this site — but it
is a ceiling, and Cloudflare does not have one.

## Recommendation

**Cloudflare Pages**, which is also what `web/` is already wired to. It is the
only option here with no traffic ceiling at all, it is free at any volume this
site will see, and the work is not a migration — it is two repository secrets.

`.github/workflows/pages.yml` already builds `web/` on every push and then skips
publishing, because `CLOUDFLARE_API_TOKEN` and `CLOUDFLARE_ACCOUNT_ID` were
never set. So the site is not published anywhere today, and the shortest path to
it being published is to set those two values.

Choose **Firebase Hosting** instead if you specifically want the site inside
Google Cloud alongside other GCP resources; the ceiling is high enough not to
matter here.

Choose **Cloud Run** only for the reasons in Option A — one deployment surface,
or server-rendered routes you intend to add. Not for cost, and not for
performance: without a load balancer it has no CDN, and with one it is the most
expensive row in the table.
