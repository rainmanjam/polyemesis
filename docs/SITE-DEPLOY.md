# Publishing polyemesis.com

The marketing site in `web/` is a static Astro build. `.github/workflows/pages.yml`
builds it on every pull request that touches `web/`, and publishes it to
Cloudflare Pages on every push to `main` — **once two repository secrets exist**.
They do not exist yet, so today that workflow builds the site, says so in a
notice, and stops. Adding the secrets is what turns it into a deploy; no edit to
the workflow is needed.

This page is the rest of it: the secrets, the DNS, and how to know it worked.

Everything below is a maintainer action against a Cloudflare account and a
registrar. None of it can be done from the repository.

## Why the site is not up now

Probed 2026-08-09 and again 2026-08-11 (recorded in
[#143](https://github.com/rainmanjam/polyemesis/issues/143)):

| Host | Result |
|---|---|
| `https://polyemesis.com` | times out, no response |
| `http://polyemesis.com` | `302` to `http://www.polyemesis.com/`, `server: namecheap-nginx` |
| `https://www.polyemesis.com` | certificate verification error |
| `https://rainmanjam.github.io/polyemesis/` | `404` |

That is a registrar parking page answering for the name, not a broken deploy —
there has never been a deploy. The certificate error on `www` is the parking host
presenting a certificate that is not for this domain.

## 1. Create the Pages project

Once, from a checkout, with a Cloudflare login:

```sh
cd web
npm ci
npx --no-install wrangler login
npx --no-install wrangler pages project create polyemesis --production-branch main
```

The name `polyemesis` is not free-form: it is `name` in `web/wrangler.toml`, it
is what the workflow deploys to, and it is what makes the default hostname
`polyemesis.pages.dev`, which the DNS records below point at.
`internal/testenv/pagesdeploy_test.go` fails if this page and `wrangler.toml`
disagree about it.

The project must exist before the first workflow run. `wrangler pages deploy`
offers to create a missing project interactively, and CI is not interactive.

## 2. Add the two repository secrets

**Settings → Secrets and variables → Actions → New repository secret.**

| Secret | Value | Where it comes from |
|---|---|---|
| `CLOUDFLARE_API_TOKEN` | an API token | Cloudflare dashboard → My Profile → API Tokens → Create Token → Custom token |
| `CLOUDFLARE_ACCOUNT_ID` | the account ID | Cloudflare dashboard sidebar, or `npx --no-install wrangler whoami` |

The token needs exactly one permission:

- **Account → Cloudflare Pages → Edit**

Nothing else. Do not use a Global API Key: it is account-wide, cannot be scoped,
and cannot be rotated without breaking every other use of it. Scope the token to
the one account, and set an expiry — the workflow fails loudly on an expired
token, which is the behaviour you want.

Both secrets are read only through `env:` in the workflow, never interpolated
into a command line.

## 3. DNS

The zone is at Namecheap today and is serving a parking page. Either route below
ends with the same two records; the first is simpler and is what the rest of this
page assumes.

### Route A — move the zone to Cloudflare (recommended)

1. Cloudflare dashboard → **Add a domain** → `polyemesis.com`, and let it import
   the existing records.
2. **Delete the parking records it imports**: the `A` record for `@` pointing at
   Namecheap's parking IP, the `CNAME` for `www` pointing at
   `parkingpage.namecheap.com`, and any URL-redirect record. Leaving them is how
   the apex keeps timing out after everything else is correct.
3. At Namecheap, set the domain's nameservers to the two Cloudflare assigns.
   Propagation is usually minutes, occasionally hours.
4. Cloudflare dashboard → **Workers & Pages → polyemesis → Custom domains → Set
   up a custom domain**, once for `polyemesis.com` and once for
   `www.polyemesis.com`.

Cloudflare creates both records itself. They should end up as:

| Type | Name | Content | Proxy status | TTL |
|---|---|---|---|---|
| CNAME | `polyemesis.com` | `polyemesis.pages.dev` | Proxied | Auto |
| CNAME | `www.polyemesis.com` | `polyemesis.pages.dev` | Proxied | Auto |

A `CNAME` at the apex is only legal because Cloudflare flattens it; that is why
route A wants the zone on Cloudflare DNS. **Proxied, not DNS-only** — the
certificate and the edge cache both depend on it, and a grey-clouded record
returns the certificate error the `www` probe already shows.

### Route B — leave DNS at Namecheap

Workable, with one caveat and one extra wait.

| Type | Host | Value |
|---|---|---|
| ALIAS | `@` | `polyemesis.pages.dev` |
| CNAME | `www` | `polyemesis.pages.dev` |

Delete Namecheap's parking `A`/`CNAME`/URL-redirect records for `@` and `www`
first, or they will win. The caveat: the apex needs Namecheap's `ALIAS` record
type, because a plain `CNAME` at an apex is not valid DNS. The extra wait:
Cloudflare issues the certificate through the Pages custom-domain flow only
*after* the record resolves, so add the custom domains in the Pages project, then
expect a few minutes of `pending` before HTTPS answers.

## 4. Deploy

Push anything to `main` that touches `web/`, or run the workflow by hand:

```sh
gh workflow run pages.yml
```

The first step of the run says whether the credential was found. If it says the
site was built but not published, one of the two secrets is missing or empty.

## 5. Verify

In order, because each answers a different question. From a shell that can reach
the internet:

```sh
# 1. Did a deployment land at all?
cd web && npx --no-install wrangler pages deployment list --project-name polyemesis

# 2. Does the apex answer over HTTPS, without --insecure?
curl -sSI https://polyemesis.com | head -1          # expect: HTTP/2 200

# 3. Does www answer, and does its certificate verify?
curl -sS -o /dev/null -w '%{http_code}\n' https://www.polyemesis.com

# 4. Did the security headers survive the move off nginx?
curl -sSI https://polyemesis.com | grep -iE 'content-security-policy|x-content-type-options|referrer-policy'

# 5. Is it the built site rather than a placeholder?
curl -sS https://polyemesis.com/sitemap-index.xml | head -3
curl -sSI https://polyemesis.com/features | head -1  # extensionless URL, expect 200
```

Check 4 is not ceremony. `web/nginx.conf` and `web/nginx-security-headers.conf`
were written for a container behind nginx; **Cloudflare Pages does not read
either file**. Every one of those headers is present only because
`web/public/_headers` restates it, the site would look completely normal without
them, and check 4 is what tells you.

Then, and only then, close [#143](https://github.com/rainmanjam/polyemesis/issues/143)'s
long-standing gate:

```sh
gh repo edit rainmanjam/polyemesis --homepage https://polyemesis.com
```

That edit has been deliberately withheld through three probes, because a sidebar
link to a domain that times out is worse than the redundant self-link it would
replace.

## What is still nginx, and why it is still here

`web/Dockerfile`, `web/nginx.conf` and `web/nginx-security-headers.conf` describe
a container behind nginx. Cloudflare Pages does not use any of them. They are
kept rather than deleted for one reason: `nginx-security-headers.conf` is the
declared header set, and
`TestThePagesHeadersRestateEverySecurityHeaderTheNginxConfigDeclares` compares
`web/public/_headers` against it name by name and value by value. While both
files exist they cannot silently drift. Deleting the nginx path is a separate
decision, and the test is what makes it safe to defer.
