# Contributing

Build with a current patch release of Go 1.26 or newer. The test suite uses synthetic documents, temporary libraries, and local fixtures. Git is needed for the Git package installation test. Tesseract enables the optional OCR smoke test.

```sh
go test ./...
go vet ./...
go test -race ./...
CGO_ENABLED=0 go build -o bin/braindex ./cmd/braindex
sh scripts/smoke.sh ./bin/braindex
```

Run the [eval regression suite](docs/evals.md) with `sh scripts/eval.sh`. Use `sh scripts/eval.sh local` to include the opt-in tests with installed local models.

Run `gofmt` on modified Go files. Use a synthetic or redistributable fixture for regressions. Keep private libraries, credentials, real email messages, and proprietary sources out of commits and issue reports.

The public interfaces are the CLI, read-only MCP tool, Codex hook, and versioned brain package format. Preserve existing library and package behavior when changing them. Keep connector services and executables optional for the core workflow.

For release candidates, see [release checks](docs/releasing.md). For a vulnerability, use the [private reporting instructions](SECURITY.md).
