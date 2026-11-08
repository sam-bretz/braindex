# Release checks

The CLI supports macOS and Linux on amd64 and arm64. Build releases with the latest security patch of a supported Go version. Keep the archives and checksums together.

```sh
go mod verify
go test -race ./...
go vet ./...
go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...
sh scripts/release.sh v0.1.0
```

The script builds without cgo and writes four archives plus a checksum manifest to `dist/`. Each archive contains the binary, license, and dependency notices. Verify from that directory with `sha256sum -c braindex_v0.1.0_checksums.txt` on Linux or `shasum -a 256 -c braindex_v0.1.0_checksums.txt` on macOS. Extract and run the binary for the host platform, check `braindex --version`, and run the synthetic smoke test from a source checkout.

Before the first distribution, verify the source tree excludes private libraries, credentials, and local trackers. Confirm a private reporting contact for collaborators. Before public distribution, enable GitHub private vulnerability reporting for the public repository. Check that CI passed on the release commit. Verify a clean installation from the target repository or release assets.

The repository starts private at `sam-bretz/braindex`. Access to a private Go module requires GitHub authentication and `GOPRIVATE=github.com/sam-bretz/braindex`. Release archives can be prepared and reviewed locally or downloaded from the manually dispatched CI workflow. Publishing a GitHub release and changing repository visibility are separate decisions.
