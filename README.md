<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="assets/logo-dark.svg">
    <img src="assets/logo.svg" alt="Externalize" height="64">
  </picture>
</p>

<p align="center"><b>exnode builds and serves Externalize proof bundles from Stellar history archives and RPC.</b></p>

<p align="center">
  <a href="https://github.com/Externalize-Labs/externalize-node/actions/workflows/ci.yml"><img src="https://github.com/Externalize-Labs/externalize-node/actions/workflows/ci.yml/badge.svg" alt="CI"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-Apache--2.0-blue" alt="License"></a>
</p>

---

A bundle packs everything needed to prove that a Stellar ledger, transaction
or Soroban contract call happened: the ledger header, the validators' SCP
messages, the ledger's results, and the call's return value and events.
[`externalize`](https://github.com/Externalize-Labs/externalize) verifies it
against the validators you trust.

**exnode is untrusted by design.** It copies archive records byte for byte and
never decides whether anything is valid; the verifier does. A compromised exnode
can refuse to answer, but it cannot produce a bundle that verifies.

## Quick start

```sh
go install github.com/Externalize-Labs/externalize-node/cmd/exnode@latest

# prove a contract call's return value and events (ledger looked up via RPC)
exnode bundle --rpc https://mainnet.sorobanrpc.com \
  --invocation 764c39734ec4da0b537f8c5e43b20223274064f84705b18943b1c35512f8da48 --out proof.json

# prove everything a contract did in one ledger
exnode bundle --rpc https://mainnet.sorobanrpc.com --ledger 64791359 \
  --contract CCNXGPE4AQCSNEBZO3XJDKKDI3CRLYMVS6UWBBTVDLALLWMJEXBORQ2A --out pool.json

externalize verify --events pool.json
```

Archives publish every 64 ledgers (about six minutes) and RPC keeps
transaction meta for roughly a week, so invocation proofs must be built in that
window. `exnode status --rpc …` shows how far the archive currently trails.
Once built, a bundle verifies forever.

## Commands

```text
exnode bundle [--ledger N] [--tx HASH]... [--invocation HASH[:OP]]... [--contract C…]... [--txset] [--out FILE]
exnode serve  [--addr :8080] [--rate 5] [--burst 20] [--cors ORIGINS]
exnode status
```

| Flag | Env | Default |
|---|---|---|
| `--network` | `EXNODE_NETWORK` | `public` (or `testnet`) |
| `--archive` | `EXNODE_ARCHIVE` | SDF's three archives for the network, comma-separated; failed or corrupt mirrors are skipped with retries and backoff |
| `--rpc` | `EXNODE_RPC` | none; needed for `--invocation`, `--contract`, or when `--ledger` is omitted |
| `--cache` | `EXNODE_CACHE` | none; checkpoint files are immutable, so cached files never expire |
| `--rate`, `--burst` | | 5 requests/s per client IP, bursts of 20, on `/v1` routes (`serve`) |
| `--cors` | `EXNODE_CORS` | none; browser origins allowed to call the API, `*` for any (`serve`) |

## HTTP API

The full contract is [`api/openapi.yaml`](api/openapi.yaml).

| Route | Returns |
|---|---|
| `GET /v1/ledgers/{seq}/bundle?tx=…&invocation=hash:op&contract=C…&txset=true` | Bundle for a ledger, with any claims |
| `GET /v1/transactions/{hash}/bundle[?op=N]` | Transaction claim, or invocation claim for op N |
| `GET /v1/status` | Archive tip, RPC latest ledger, and the lag between them |
| `GET /healthz` | Liveness |
| `GET /metrics` | Prometheus metrics: requests by status class, bundles built and failed |

Bundles carry a strong `ETag` (`If-None-Match` gets a 304), are cacheable
forever (`Cache-Control: immutable`), and are gzipped for clients that accept it
(about 3.3x smaller). Every response has an `X-Request-ID`, and every request is
logged as structured JSON. Errors are JSON `{"error": "…"}`: 400 bad input, 404
not archived yet or outside RPC retention, 429 rate limited (with
`Retry-After`), 501 no RPC configured, 502 upstream failure.

```sh
docker build -t exnode .
docker run -p 8080:8080 -e EXNODE_RPC=https://mainnet.sorobanrpc.com exnode
```

## Conformance

`testdata/bundle-64791359.json` is externalize-core's own fixture. The tests
build the same bundle from a local copy of the mainnet archive layout through
the real archive and RPC clients, and require byte equality. CI also checks
that the copy matches upstream.

## Layout

| Package | Responsibility |
|---|---|
| `internal/archive` | Checkpoint math, record framing (fuzzed), mirror failover, disk and memory caches |
| `internal/rpc` | `getTransaction`, `getEvents` and `getLatestLedger` client |
| `internal/bundle` | Request validation, bundle assembly, invocation extraction from meta |
| `internal/server` | HTTP routes, error mapping, ETags and gzip, metrics, rate limiting, CORS, access logs |
| `cmd/exnode` | CLI |

## Contributing

Run `gofmt`, `go vet ./...` and `go test -race ./...` before opening a PR.
Issues are scoped for the Stellar Wave program; comment to get assigned
first. See the verifier's
[CONTRIBUTING.md](https://github.com/Externalize-Labs/externalize/blob/main/CONTRIBUTING.md)
for the ground rules shared across Externalize repositories.

## License

[Apache-2.0](LICENSE)
