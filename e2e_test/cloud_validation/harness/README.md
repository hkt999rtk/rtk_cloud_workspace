# Harness ownership

The executable harness implementation is the Go package in
`../cloudvalidation/` and its CLI in `../cmd/cloud-validation/`. This directory
documents the orchestration boundary so provider scripts, sample apps, and
virtual-device code do not grow a second result/status implementation.

The Go harness exclusively owns scenario loading, timeouts, status aggregation,
Cloud-evidence validation, cleanup finalization, JUnit, JSON, and Markdown
reports. Shell scripts may launch tools and collect artifacts, but they must not
convert a failed or blocked step into `PASS`.
