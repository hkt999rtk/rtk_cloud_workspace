# CI Flight Deck

CI Flight Deck is a local-only dashboard for GitHub Actions runs in the RTK cloud workspace and its first-level RTK submodules. It is not a product service and has no deployment target.

## Run locally

From this directory:

```sh
GOWORK=off go run .
```

Then open <http://127.0.0.1:8787>.

The server finds the workspace by walking upward to `.gitmodules`. You can run it elsewhere by passing the workspace explicitly:

```sh
GOWORK=off go run . -workspace /path/to/rtk_cloud_workspace0
```

Useful flags:

```text
-address 127.0.0.1:8787
-poll-interval 1m
-github-api https://api.github.com
-workspace /path/to/workspace
```

The browser reads a cached snapshot every five seconds. The Go server polls GitHub once per minute by default and updates elapsed counters without querying GitHub every second.

## Authentication and repository scope

Authentication is resolved in this order:

1. `GITHUB_TOKEN`
2. The token returned by `gh auth token`

The token needs read access to Actions and pull requests in the private RTK repositories. It stays in the Go process and is never included in browser responses or logs.

The dashboard includes the workspace repository and first-level `.gitmodules` entries owned by `hkt999rtk`. Nested third-party repositories such as `emqx/emqx-docker` are intentionally excluded.

## Behavior

- Runs are grouped into Queued, Running, and Completed lanes.
- Completed is a global window of 20 job cards ordered strictly by job completion time, newest first. Status color remains visible but does not change the order.
- A GitHub re-run retains the same card because the key is `owner/repo/run_id`. The drawer shows each `run_attempt` separately.
- A fresh push or manual dispatch creates a new card because GitHub assigns a new `run_id`.
- Manual runs use GitHub's `display_title`; the fallback is workflow, actor, branch, and short SHA.
- A failed repository refresh preserves its last snapshot and marks its cards `STALE`.
- Failure, timeout, startup failure, and action-required conclusions are red. Long-running active cards keep a blue Running state while their elapsed-time rail heats from green to red.

## Local API

- `GET /api/snapshot` returns repository health, rate-limit information, and the three card lanes.
- `GET /api/runs/{owner}/{repo}/{runID}` loads attempt-specific jobs and steps for the detail drawer.
- `GET /healthz` returns a local process health response.

## Tests

```sh
GOWORK=off go test ./...
GOWORK=off go vet ./...
GOWORK=off go build ./...
```

The dedicated GitHub Actions workflow runs only for this directory and does not deploy the tool or add it to the workspace product test matrix.
