# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project uses
[Semantic Versioning](https://semver.org/).

## [Unreleased]

### Changed

- **Breaking:** bundles are now `externalize/bundle/v2`, with two new required
  fields: `next_scp` (ledger N+1's `ScpHistoryEntry`) and `next_transactions`
  (ledger N+1's `TransactionHistoryEntry`). Validators sign only a ledger's
  `StellarValue`, so v1 bundles let the rest of the header
  (`txSetResultHash`, `previousLedgerHash`) be forged; N+1's externalized
  transaction set commits to `SHA-256(header N)`. Every route, `exnode bundle`
  and the Vercel function emit v2. Verifiers that only accept v1 reject these
  bundles.
- A checkpoint's last ledger cannot be bundled until the next checkpoint is
  published: `exnode bundle` fails with a retry message and the HTTP routes
  answer 503 with `Retry-After` (never cached).
- `GET /v1/ledgers/latest/bundle` redirects to the ledger before the archive
  tip, the newest whose successor is archived. `exnode status` prints it.
- `testdata/bundle-64791359.json` was regenerated as v2 from SDF's mainnet
  archive.

### Added

- `txset=true` on `GET /v1/transactions/{hash}/bundle`, as on the ledger route.

## [0.2.0] - 2026-10-28

### Added

- Serverless deployment: `api/exnode.go` runs the HTTP server as a Vercel
  function configured from the usual `EXNODE_*` variables, with `vercel.json`.
- `--client-ip-header` (`EXNODE_CLIENT_IP_HEADER`): rate-limit on a header set
  by the proxy in front of the node, such as Vercel's `X-Real-Ip`.

### Changed

- Bundles are served with `s-maxage` as well, so CDNs cache them like browsers.
- The network table moved from `cmd/exnode` into `internal/network`.

## [0.1.0] - 2026-10-07

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

[Unreleased]: https://github.com/Externalize-Labs/externalize-node/compare/v0.2.0...HEAD
[0.2.0]: https://github.com/Externalize-Labs/externalize-node/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/Externalize-Labs/externalize-node/releases/tag/v0.1.0
