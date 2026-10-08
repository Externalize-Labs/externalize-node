# Contributing

`exnode` builds Externalize proof bundles from history archives and Stellar RPC.
It takes part in the [Stellar Wave](https://www.drips.network/wave/stellar)
program; Wave issues are labeled with their complexity. The ground rules for
every Externalize repository are in the
[organization guide](https://github.com/Externalize-Labs/.github/blob/main/CONTRIBUTING.md).

## Setup

```sh
git clone https://github.com/Externalize-Labs/externalize-node && cd externalize-node
go test ./...
```

Go 1.25 or later. Tests run offline against recorded archive and RPC responses
in `testdata/`; nothing reaches the network.

## Before you open a PR

```sh
gofmt -l .            # must print nothing
go vet ./...
go test -race ./...
golangci-lint run
```

CI also runs govulncheck, builds the Docker image, and checks the bundle
conformance fixture.

## Fixtures

`testdata/bundle-64791359.json` is a mainnet bundle that must stay byte-identical
to [externalize](https://github.com/Externalize-Labs/externalize)'s
`crates/externalize-core/tests/fixtures/mainnet/bundle-64791359.json`; CI
compares them. A bundle format change lands in externalize first, then here.

## Commit messages

[Conventional Commits](https://www.conventionalcommits.org): `feat(archive): …`,
`fix(rpc): …`, `docs: …`.
