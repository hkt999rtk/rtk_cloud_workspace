# Search indexing policy

All RTK managed service environments, including dev, staging and prod, must
remain excluded from crawling and search indexing until explicitly approved.
The deployment generator enforces this policy independently of service images.

## Managed HTTPS ingress

`scripts/go/rtk-cloud/k8s_noindex.go` configures ingress-nginx with:

- `X-Robots-Tag: noindex, nofollow, noarchive, nosnippet, noimageindex` on
  responses, including redirects, API responses, static resources and errors.
- `/robots.txt` returning plain text with `User-agent: *` and `Disallow: /`.
  This covers compliant search and AI crawlers. Explicit AdsBot agents share
  the same disallow group because they can ignore the wildcard group.
- Root sitemap XML files, compressed sitemap XML, sitemap indexes and the
  requested `/sites.xml` alias returning 404 instead of publishing URLs.
- Frontend deployments always setting `DISABLE_SEARCH_INDEXING=true`, which
  also emits HTML robots metadata and disables the application's sitemap.

The controller setting applies to every host on the managed ingress, including
future routes. HAProxy forwards TLS at the TCP layer, so HTTP policy belongs at
ingress-nginx. Device client-certificate verification remains mandatory; an
unauthenticated request can receive 400 before reaching robots.txt.

No tracked `sites.xml` exists. The frontend dynamically generates `/sitemap.xml`
when indexing is enabled; changing an XML file would not enforce this policy.

## Live verification — 2026-09-04

Applied the controller ConfigMap policy and frontend environment setting to dev
and staging without changing application images. NGINX syntax checks and both
frontend rollouts passed. Local `go test ./scripts/go/rtk-cloud -run
'Test(LKE|K8S)' -count=1` passed.

The following hosts were checked under **both**
`video-cloud-dev.realtekconnect.com` and
`video-cloud-staging.realtekconnect.com`:

| Host prefix | Service |
| --- | --- |
| (none) | Video Cloud API |
| account-manager | Account Manager |
| admin | Admin portal |
| billing | Billing |
| certissuer | Certificate issuer |
| device | Device mTLS API |
| frontend | Frontend website |
| logger | Cloud logger |
| payment-simulator | Payment simulator |
| turnregistry | TURN registry |

For every host, checked `/`, `/robots.txt`, `/sitemap.xml`, `/sitemap_index.xml`,
`/sitemap.xml.gz`, `/sites.xml`, `/noindex-verification-missing`, and `/healthz`.
All 160 responses passed indexing-policy checks. Both frontend homepages also
contained robots meta tags. Raw results are in `.artifacts/noindex/dev.json`
and `.artifacts/noindex/staging.json` (local, not committed).

These are indexing checks, not a full health certification: the certificate
issuer root returned 502 in both environments. Device requests without a client
certificate returned 400 as expected. Video API `/healthz` returned 200.

## Remaining external coverage

| Site/environment | Finding | Remaining work |
| --- | --- | --- |
| `sm.realtekconnect.com` | `/robots.txt` returned 404 without X-Robots-Tag | Identify its owning reverse-proxy configuration and apply the same policy. This is an external sendmail dependency, outside the managed LKE ingress. |
| `webtest.mgmeet.io` | Normal TLS validation failed; unauthenticated diagnostic GETs without certificate verification returned 403 for the homepage, robots and sitemap, without X-Robots-Tag | Identify its owning reverse proxy and verify access restrictions, certificate chain and explicit indexing policy. No credentials were sent. |
| prod | Deployment generator covered; no local prod kubeconfig found | Verify the actual production deployment before claiming live coverage. |

## Limits and existing search results

Robots directives govern cooperative crawlers; they do not enforce access
control against arbitrary clients. Retain authentication/mTLS for private data.
Do not claim that these directives guarantee a URL can never appear in search.

Google documents that a URL blocked by robots.txt may still appear when linked
elsewhere, and a blocked crawler cannot read the page's noindex header. If a URL
is already indexed, use the search engine's verified site-owner removal process
and plan permanent removal/access restriction, or an explicitly approved period
of crawling so noindex can be observed. Do not silently relax this project's
no-crawl policy to perform removal.

References:

- [Google robots.txt limitations](https://developers.google.com/search/docs/crawling-indexing/robots/intro)
- [Google robots metadata and response headers](https://developers.google.com/search/docs/crawling-indexing/robots-meta-tag)
- [Ingress-nginx controller configuration](https://kubernetes.github.io/ingress-nginx/user-guide/nginx-configuration/configmap/)
