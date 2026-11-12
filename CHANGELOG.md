# Changelog

## v0.1.0-rc.6

- Retry Homebrew release draft discovery and use current Go patches in CI.

## v0.1.0-rc.5

- Add a standalone `braindex demo` that retrieves two independently cited passages and reopens a source revision.
- Add deterministic and local-model evals, opt-in ranker comparison, and local inbox destination suggestions.
- Save public PDF links as references and apply page and extracted-text limits to trusted local PDF imports.

## v0.1.0-rc.2

- Preview a link and route selected articles, PDFs, feed items, or videos to a brain or the inbox.
- Follow feeds, podcasts, YouTube series, or authors, then refresh and process selected items.
- Discover an author's work, optionally transcribe podcast audio locally, and attach timestamped YouTube captions.
- Review staged captures interactively, hand selected items to an agent, and preview or apply a batch of placements.
- Explain topic routing and surface separately cited evidence from multiple brains.
- Preview shared brain packages, compare their source inventories, and confirm a verified upgrade.

- Add a read-only `braindex status` command for library and index diagnostics.
- Add a labeled cross-brain retrieval benchmark and a Codex citation walkthrough.

## v0.1.0 candidate

- Import local documents and supported connectors into user-managed brains.
- Retrieve cited evidence through the CLI, MCP, and the Codex hook.
- Offer optional local embeddings and hybrid search across selected brains.
- Stage captures in a private inbox and review before adding them to a brain.
- Export and install versioned brain packages from directories, Git, and catalogs.
- Build standalone macOS and Linux archives with checksums and dependency notices.
