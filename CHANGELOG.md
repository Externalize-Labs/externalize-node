# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project uses
[Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added

- `exnode bundle` and `exnode serve`: build proof bundles from history archives
  and Stellar RPC, byte-identical to externalize-core's conformance fixture.
- Contract-wide bundles: `--contract C…` proves every invocation in a ledger
  that emitted events from the contract.
- Archive resilience: SDF's three mirrors by default, retries with exponential
  backoff, corrupt-file detection, concurrent category fetches, disk and
  in-memory caches.
- `exnode status` and `GET /v1/status`: archive tip and lag behind RPC.
- `GET /v1/ledgers/latest/bundle`: redirect to the newest archived ledger.
- HTTP: strong ETags with 304s, gzip, request IDs and structured access logs,
  Prometheus metrics, per-client rate limiting, CORS, build timeouts.
- `--out` writes bundles atomically.
- OpenAPI 3.1 description in `api/openapi.yaml`.
- Release pipeline: GoReleaser binaries for Linux, macOS and Windows, and a
  multi-arch container image on GHCR.

### Security

- Bumped `klauspost/compress` to 1.18.7 (GO-2026-5841; not reachable from exnode).

[Unreleased]: https://github.com/Externalize-Labs/externalize-node/commits/main
