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

## Publish macOS binaries to Homebrew

Set up the tap once. Create a fine-grained GitHub personal access token with
access to **only** `sam-bretz/homebrew-tap` and **Contents: Read and write**.
In the private Braindex repository, save it as the `HOMEBREW_TAP_TOKEN`
[Actions repository secret](https://github.com/sam-bretz/braindex/settings/secrets/actions).
Rotate it before it expires.

For each new release, tag a commit that includes `.github/workflows/homebrew.yml`.
Attach the files below from the verified candidate to a draft release, then
publish the release in GitHub. Replace `VERSION` with the tag, including the
leading `v`.

- `braindex_VERSION_darwin_arm64.tar.gz`
- `braindex_VERSION_darwin_amd64.tar.gz`
- `braindex_VERSION_checksums.txt`

Publishing the private release also publishes the two macOS archives in
[the public tap](https://github.com/sam-bretz/homebrew-tap/releases). The workflow
verifies both checksums before publishing and then updates `Formula/braindex.rb`.
The tap receives only the two macOS archives and formula metadata. Use a new tag
when rebuilding a candidate. Older versions are skipped once the formula pins a
newer version.

Check **Actions → Publish to Homebrew** for the result. If the release was
published before the archives or secret were ready, run that workflow manually
with the same tag after fixing the setup. Then verify the installed version with
`brew update`, `brew install sam-bretz/tap/braindex` (or `brew upgrade sam-bretz/tap/braindex`
if already installed), and `braindex --version`.
