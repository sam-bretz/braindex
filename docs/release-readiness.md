# Braindex release readiness

**Scope** Local review of the first release candidate for `sam-bretz/braindex`  
**Distribution** MIT. The intended GitHub repository starts private.

## Comparison

GitHub star counts are approximate snapshots. The comparisons focus on what a new user can install, verify, and understand.

| Project | What it demonstrates | What Braindex needs at this stage |
| --- | --- | --- |
| [QMD](https://github.com/tobi/qmd), about 31k stars | The closest comparison. A short local search quickstart, released packages, keyword and semantic search, reranking, and agent setup documentation. | Keep the first query reproducible from a standalone binary. Measure retrieval quality and latency across representative libraries before making comparative claims. |
| [Khoj](https://github.com/khoj-ai/khoj), about 38k stars | A broader self-hosted second brain with documented installation, multiple clients, and contributor guidance. | Document which integrations require a local app or credentials. Run one end-to-end test in a normally configured coding-agent host. |
| [AnythingLLM](https://github.com/Mintplex-Labs/anything-llm), about 68k stars | Platform-specific installers, clear setup and privacy guidance, a contributor guide, and a published security policy. | Give readers platform-specific archives and a clear privacy boundary. Validate the GitHub distribution path and turn on private vulnerability reporting before inviting public use. |

Braindex's current product boundary is a CLI for source-backed retrieval across explicitly selected brains, plus portable data packages. Hosted chat, an enterprise connector estate, and a public marketplace are separate product work.

## Dependency review

The binary has **four direct Go module dependencies and 21 linked external modules** in the reviewed macOS build. Each direct module covers a distinct capability.

| Module | Why it is included |
| --- | --- |
| `modernc.org/sqlite` | SQLite FTS5 and persistent vector storage without cgo or a database service. |
| `golang.org/x/net` | HTML parsing and email charset handling. |
| `github.com/razvandimescu/gopdf` | Embedded PDF text extraction with page locators. |
| `github.com/modelcontextprotocol/go-sdk` | The MCP wire protocol and tool schema. |

Keyword search and the synthetic quickstart run without Ollama, provider credentials, a separate database, or an external runtime. Ollama, Git, Tesseract, and Heptabase are feature-specific runtime dependencies. Release archives include the notices of the linked modules and the Go runtime.

The advisory scan found a reachable HTML parser denial of service. The upgrade to `x/net v0.62.0` fixed it. The subsequent symbol-level scan using Go 1.27.3 reported no vulnerabilities. [Go vulnerability report](https://pkg.go.dev/vuln/GO-2026-6860)

## Code and release review

- **Package trust.** Catalog archives and package assets have checksum and size checks. Git installation now verifies the pinned tag and materializes only bounded regular manifest and source files. Git servers without partial clone support can still send more repository data during the clone. Publisher attribution is supplied by the catalog.
- **Private data.** The default Git exclusions cover Braindex library metadata, the project-local private library, local tracker state, and common credential filenames. The quickstart writes to a temporary library. The contributor and security docs explain what goes into a report and the difference between retrieval policy and filesystem access.
- **Build and distribution.** The local release script produces cgo-free macOS and Linux archives for amd64 and arm64, with checksums and notices. CI is defined with read-only permissions, pinned actions, native smoke tests, race tests, and advisory checks. A manually dispatched job produces reviewable artifacts.
- **Retrieval performance.** A local synthetic benchmark on macOS arm64 measured median end-to-end keyword queries of 11 ms at 1,000 passages and 28 ms at 10,000 passages, over seven runs per size. These are single-source fixture measurements. The query path still loads source snapshots, and vector search scans the selected vectors. Larger real libraries and semantic relevance need separate measurement.

The combined Go coverage run exercised 65.1% of statements. Connector error paths and full agent-host behavior remain areas for more end-to-end coverage.

## Adoption follow-up

- `braindex status` reports library setup, source availability, and keyword and embedding index freshness. The quickstart shows an expected citation, and the changelog records candidate changes.
- The [labeled retrieval benchmark](retrieval-benchmark.md) compares keyword and hybrid modes on a small multi-brain fixture.
- An authenticated Codex CLI session used a temporary Braindex MCP registration, invoked `braindex_context`, and cited the synthetic calibration marker from `calibration`, line 1. The [agent walkthrough](agent-integration.md#verify-a-citation-in-codex) reproduces the setup.

## Remaining checks

1. Run the workflow in the intended private GitHub repository. Verify tagged `go install`, release downloads, and the collaborator reporting path. GitHub private vulnerability reporting applies once the repository is public. Locally, all four targets built and the extracted macOS arm64 binary passed the smoke test.
2. Finish the Gmail pilot after the owner configures a Desktop OAuth client and selects a label. Keep live connector credentials out of the release validation workflow.
3. For a later marketplace rollout, verify publisher identity, support in-place package upgrades, and establish moderation and download withdrawal procedures. The current catalog is an installable static pilot.

## Public-launch preparation

A read-only check covered 150 unique Git history blobs and all 18 Actions logs
listed for the private repository at the time of review. It checked tracked
and launch candidate paths for private libraries and credential filenames and
scanned text for common private key and provider token formats. The scan
reported zero matches. This check does not detect all secrets or establish
redistribution rights.

Downloaded RC1 and RC2 each had four archives and a SHA-256 manifest. All
eight archive checksums matched. The corresponding Actions artifacts contained
byte-identical archives. Tar contents were limited to the executable,
README, project license, and third-party notices. Only synthetic sources are
compiled into the standalone demo.

The public PDF link path now saves references and avoids invoking the PDF
parser. Explicit trusted local import retains page citations and applies file,
page, and extracted-text bounds. The dependency still has no hard
decoded-stream or per-page CPU cap. See [Security](../SECURITY.md).

The repository remains private. Its private vulnerability reporting form
cannot be verified as an outside reporter until the public transition. Use the
[public release procedure](releasing.md#prepare-the-public-release) to verify
reporting and anonymous downloads when publication is authorized.

The local RC3 build produced four verified checksums. The extracted ARM64
binaries passed the standalone demo on macOS and in a fresh Ubuntu VM. Both
ran from empty directories with a clean environment and no tools on PATH.
The Linux run additionally used an isolated network namespace. The amd64
archives were cross-built and checked by hash. The candidate is available in
`dist/` from the preparation checkout.
