# Contributing

Open an [issue](https://github.com/Adanomy/carla-exporter/issues) for bugs and
feature proposals. For vulnerabilities, follow [SECURITY.md](SECURITY.md).

## Checks

Use **Go 1.27.3** and a C compiler for the race detector. Run from the repository
root:

```sh
gofmt -w cmd internal tests
go vet ./...
go test -race ./...
CGO_ENABLED=0 go build -trimpath -o bin/carla-exporter ./cmd/carla-exporter
python3 -m unittest discover -s grafana
```

Dashboard query tests require Python 3 and either `promtool` or Docker. They evaluate the saved PromQL against restart, duplicate, conflict, and missing-data scenarios.

The default tests use local wire servers and require no CARLA installation.
Set `CARLA_ADDRESS=host:port` to enable the read-only live test. Optional
`CARLA_STREAM_ADDRESS` overrides the stream endpoint, `CARLA_EXPECT_VERSION`
checks the release, and `CARLA_EXPECT_FRAMES=1` requires a snapshot from a running
simulation.

Protocol changes need release-specific fixtures and regression tests. See
[fixture sources and regeneration](testdata/protocol/README.md); Python 3 is
needed only to regenerate them. To fuzz a decoder:

```sh
go test ./internal/carla/protocol -run '^$' -fuzz '^FuzzDecodeWorld$' -fuzztime=30s
```

## Pull requests

- Keep each PR focused and describe the problem, resulting behavior, and checks run.
- Add tests for behavior changes and update affected documentation.
- Keep code comments, logs, errors, documentation, and PR descriptions in English.
- Preserve read-only CARLA access; add no tick, control, or world-setting writes.
- Describe user-visible changes and compatibility impacts in the PR. Release
  notes are published in [GitHub Releases](https://github.com/Adanomy/carla-exporter/releases).

Use [Conventional Commits 1.0.0](https://www.conventionalcommits.org/en/v1.0.0/)
for commit messages and PR titles, with a short English description:

```text
feat(protocol): support another CARLA release
fix(exporter): discard stale vehicle metrics
docs: clarify stream configuration
```

Mark breaking changes with `!` and explain them in the description. Contributions
are distributed under the project's [MIT license](LICENSE).
