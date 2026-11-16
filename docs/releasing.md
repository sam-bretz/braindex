# Release checks

The CLI supports macOS and Linux on amd64 and arm64. Build releases with the latest security patch of a supported Go version. Keep the archives and checksums together.

## Run a local pre-release check

```sh
sh scripts/pre-release.sh v0.1.0-rc.4
sh scripts/pre-release.sh v0.1.0-rc.5 --local-evals
```

The runner snapshots tracked files and non-ignored new files from the working
tree, including local edits. It checks formatting, module integrity, vet, the
full race suite, Gmail OAuth and sync fixtures, saved-email parsing, evals,
and Go advisories. It then builds all four archives, verifies their checksums,
and smoke-tests the extracted native binary. A successful run writes the
candidate to `dist/prerelease/VERSION/`. Use a fresh version for another run.

The default eval suite uses fixtures. With `--local-evals`, start Ollama on
loopback with the models in the [eval guide](evals.md) already installed.
Gmail checks use test fixtures and require no Google account. The runner
does not read a real mailbox or send mail. Go may fetch dependencies and the
advisory database. The script builds locally and does not publish a release.
Run it on both supported operating systems for native smoke coverage of each.

For packaging alone, run `sh scripts/release.sh VERSION`.

The script builds without cgo and writes four archives plus a checksum manifest to `dist/`. Each archive contains the binary, license, and dependency notices. Verify from that directory with `sha256sum -c braindex_v0.1.0_checksums.txt` on Linux or `shasum -a 256 -c braindex_v0.1.0_checksums.txt` on macOS. Extract the binary into an empty directory, run `./braindex --version`, and run
`./braindex demo`. The demo prints two separately cited brains and reopens one
stored source revision. It requires only the extracted binary and a writable
temporary directory. From a source checkout, `sh scripts/smoke.sh PATH_TO_BINARY`
also verifies this flow in an empty directory with a clean environment before
checking an imported file.

Before the first distribution, verify the source tree excludes private libraries, credentials, and local trackers. Confirm a private reporting contact for collaborators. Before public distribution, enable GitHub private vulnerability reporting for the public repository. Check that CI passed on the release commit. Verify a clean installation from the target repository or release assets.

The repository starts private at `sam-bretz/braindex`. Access to a private Go module requires GitHub authentication and `GOPRIVATE=github.com/sam-bretz/braindex`. Release archives can be prepared and reviewed locally or downloaded from the manually dispatched CI workflow. Publishing a GitHub release and changing repository visibility are separate decisions.

## Prepare the public release

Keep the candidate private while rehearsing this sequence.

1. Pick a reviewed source commit and build the four candidate archives from
   that commit. Check the version string, verify the checksum file, and run
   the demo from an empty directory on macOS and Linux. Check that the
   first README download points to a release containing the demo.
2. Review tracked history and the exact candidate diff for private libraries,
   credentials, and content you cannot redistribute. Check existing releases,
   workflow logs, and uploaded artifacts. GitHub exposes Actions history and
   logs on a visibility change. [GitHub visibility documentation](https://docs.github.com/en/repositories/managing-your-repositorys-settings-and-features/managing-repository-settings/setting-repository-visibility)
3. Once publication is authorized, make the repository public and enable
   private vulnerability reporting in **Settings → Security and quality →
   Advanced Security**. Open the reporting form from a separate account and
   confirm that it offers a private report. Check the release page and README
   from an anonymous session and download an archive. [GitHub reporting documentation](https://docs.github.com/en/code-security/how-tos/report-and-fix-vulnerabilities/configure-vulnerability-reporting/configure-for-a-repository)
4. Verify the new tag and downloads resolve without collaborator access.
   Run the demo from the downloaded archive again. Check that the repo
   description, topics, and social preview show the intended project identity.

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
