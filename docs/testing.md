# Cross-Repository Testing

Use this workspace to coordinate validation across pinned submodule commits.

## Local Baseline

```sh
./scripts/status-all.sh
./scripts/docs-check.sh
./scripts/test-matrix.sh
```

`docs-check.sh` is read-only and validates documentation governance assumptions:
workspace repository entries, key docs entry points, and contracts submodule
commit alignment.

## LAN Interop

Known LAN roles used by the client/server project:

- `github-runner.local`: deployed video-cloud test server.
- `client-a.local`: Linux-only native/Node transport owner role.
- `client-b.local`: Linux-only replacement owner and routing-isolation role.

Credentials, tokens, device ids, and generated test artifacts must stay outside
the repository and be passed through environment variables or local temp files.
