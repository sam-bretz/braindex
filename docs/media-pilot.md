# Media and author discovery pilot

These five fictional personas are test cases. Each represents a different way a user might decide which sources to trust.

| Persona | What they want to answer | What they would collect |
| --- | --- | --- |
| Early-stage founder | Which advice applies to the sales call or hiring decision in front of me? | Founding Sales chapters or an owned ebook, founder blogs and newsletters, podcast episodes and interviews, YC playlists, playbook PDFs, pitch decks, and their own call notes and screenshots. |
| Staff software engineer | Which standards, project decisions, and examples should an agent consult before changing this system? | Markdown docs and ADRs, selected repo files, release notes and Atom feeds, RFCs and PDFs, maintainer talks and conference playlists, issue discussions, diagrams, and internal incident writeups. |
| AI researcher | What evidence and methods support this claim, and where can I verify it? | Paper PDFs and preprints, arXiv or journal feeds, author bibliographies and DOI links, experiment notebooks and CSVs, conference videos and slides, benchmark plots, and reading notes. |
| Independent markets analyst | What did my trusted analysts say, and when did their thesis change? | Saved newsletter emails, a dedicated Gmail label, RSS research, filings and investor PDFs, earnings-call audio and transcripts, chart screenshots, spreadsheets, and notes. |
| Educator and independent learner | What explanation or example should I revisit when I get stuck? | Textbook chapters and owned EPUBs, course YouTube playlists, lecture audio and captions, course pages and slides, handouts and Word docs, scanned worksheets, whiteboard notes, and flashcard exports. |

## What the CLI actually ingested

Tests used disposable libraries. Network sources were public. Email, image, and document fixtures were synthetic. The separate Dalton + Michael pilot used a private local library.

| Input | Observed result |
| --- | --- |
| Public HTML chapter and publisher article | The [Founding Sales introduction](https://www.foundingsales.com/introduction) and [First Round article](https://review.firstround.com/to-build-an-amazing-sales-team-start-here-first/) produced searchable text and extracted-page line citations. The [YC transcript page](https://www.ycombinator.com/library/IP-dalton-michael-secrets-you-can-learn-from-your-customers) yielded an unavailable source with the script-free importer. |
| Atom feed | The [Go Blog](https://go.dev/blog/) feed yielded 10 searchable entries and feed-entry line citations. |
| Text PDF | [Attention Is All You Need](https://arxiv.org/abs/1706.03762) yielded search results with page and line locators. |
| Markdown, HTML email, image | A Markdown note yielded a line locator, an EML yielded an email-body locator, and Tesseract returned a searchable image region. |
| Podcast RSS and audio | The [Dalton + Michael feed](https://anchor.fm/s/109e61694/podcast/rss) yielded 41 episode records and no transcript links. Five offline Whisper transcripts produced 312 timestamped passages and survived a feed refresh. Direct MP3 import was unsupported. |
| EPUB and DOCX | Direct imports were unsupported. Pandoc 3.7.0.2 converted both synthetic files to Markdown that Braindex imported. A converted source uses Markdown line locators. |
| CSV | Direct import was unsupported. This pilot did not evaluate structured retrieval or preservation of row and column locators. |
| YouTube series and captions | [Dalton + Michael's official channel](https://www.youtube.com/@daltonplusmichael/videos) yielded eight requested video records in 0.70 seconds with a warm yt-dlp 2026.10.26 invocation. One auto-caption track fetched in 2.42 seconds. The other attempt returned HTTP 429. A single channel RSS request returned HTTP 404. Raw import of the successful VTT yielded 1,700 cues, with repeated or extremely short adjacent search hits. |

## Implementation decisions

- **YouTube.** Keep the supported [YouTube Data API playlist connector](https://developers.google.com/youtube/v3/docs/playlistItems/list) for metadata. It needs a key, and [official caption downloads](https://developers.google.com/youtube/v3/docs/captions/download) require permission to edit the video. The optional [yt-dlp](https://github.com/yt-dlp/yt-dlp) subprocess discovers public playlists and channels. Caption fetching is a separate step. Braindex caches successful tracks, stops fresh requests when rate limited, and normalizes rolling captions into larger timestamped passages. The measured run used the current extractor, its JavaScript components, and Node.
- **Audio.** The podcast adapter runs a configured local executable that writes WebVTT. It downloads only selected HTTPS episodes, caches the attached text, and rebuilds the keyword index. Feed refreshes stay metadata-only and preserve attached transcripts.
- **Documents.** Keep the existing PDF parser and optional Tesseract. Offer Pandoc as a conversion path for DRM-free EPUBs and Word documents. A native converter is worth considering when chapter or section locators become important. Treat structured tables, scanned PDFs, and slides as separate evaluation cases.
- **Web.** Keep the simple static HTML parser for ordinary articles. Test readability extraction on pages where navigation dominates, and offer a separate browser capture or paste workflow for rendered content. A parser for static HTML has no body to extract from an empty app shell.
- **Author catalogs.** If book and paper attribution needs greater precision, add explicit identity resolution using an [Open Library author ID](https://openlibrary.org/dev/docs/api/authors) or [ORCID](https://info.orcid.org/what-is-orcid/). Search results today are candidates for a person, including appearances and mentions.

## Fetch selected YouTube captions

Install [yt-dlp](https://github.com/yt-dlp/yt-dlp) separately and put it on the command path. Set `BRAINDEX_YT_DLP` to a custom executable if needed. For a current yt-dlp installation using Node for JavaScript extraction, set `BRAINDEX_YT_DLP_JS_RUNTIME=node`.

```sh
braindex discover youtube https://www.youtube.com/@daltonplusmichael/videos \
  --yt-dlp --limit 8 --brain dalton-michael --create-brain
braindex source youtube-captions SOURCE_ID --lang en
braindex index rebuild
```

Discovery prints stable source IDs and video URLs. Pass up to ten selected IDs to fetch a caption track. Braindex stores successful VTT tracks inside the library, reuses them on retry, and reports whether a track is searchable, unavailable, rate limited, or failed. On a rate limit, it serves cached tracks and skips further downloads in the batch. Raw rolling cues are normalized when attached. Reattach a previously imported transcript to apply normalization to its current revision.

## Transcribe selected podcast episodes locally

Discover or refresh the feed first so Braindex records the audio enclosure URL. Pick episode IDs from the resulting list.

```sh
braindex discover podcast https://anchor.fm/s/109e61694/podcast/rss \
  --brain dalton-michael --create-brain
braindex source transcribe SOURCE_ID --transcriber /path/to/local-transcriber
```

The default executable contract is two positional arguments, an input audio path and an output VTT path. You can set `BRAINDEX_TRANSCRIBER` or supply repeated `--arg` values with `{input}`, `{output}`, and `{dir}` placeholders. For example, with an installed MLX Whisper executable and a cached local model:

```sh
HF_HUB_OFFLINE=1 braindex source transcribe SOURCE_ID --transcriber mlx_whisper \
  '--arg={input}' --arg=--model --arg /absolute/path/to/model \
  --arg=--output-name --arg=transcript --arg=--output-dir '--arg={dir}' \
  --arg=--output-format --arg=vtt --arg=--language --arg=en --arg=--verbose --arg=False
```

Braindex passes arguments directly to the selected executable. It downloads public HTTPS audio enclosures up to 200 MiB, removes the temporary audio, and retains the validated transcript. Pass up to twenty episode IDs to process a batch. Results identify each success or failure, and rerunning skips episodes with attached text. Use `--force` to replace an attachment after a successful transcription and `--timeout` to change the 20-minute budget per episode. A failed forced run preserves the existing text. Review the output of an automatic transcript before relying on a quote.

## Discover and review work by a person

```sh
braindex brain create pete-kazanjy --name "Pete Kazanjy"
braindex discover person "Pete Kazanjy" --brain pete-kazanjy \
  --hint "Founding Sales" --seed https://www.foundingsales.com/introduction
```

With `BRAVE_SEARCH_API_KEY` configured, Braindex makes four targeted [Brave Search API](https://api-dashboard.search.brave.com/documentation/quickstart) requests for articles, books, podcast appearances, and videos. The provider receives the name and hint. The seed URL is placed in the local review list. Use a public role, topic, or affiliation for the hint. Search can miss work or surface a different person with the same name. Review the titles and original URLs, then enter comma-separated result numbers. Press Enter to skip. `--json` previews the results without saving.

Selections become references in the chosen brain. Add `--import-articles` to contact selected article sites and fetch text. If a page cannot be extracted, its link is still saved. Books and podcast or video results stay references until you supply readable content through the relevant connector or a local file. Run `braindex index rebuild` after importing text.

For a key-free workflow, give the command a local `--results FILE` from a search tool or agent. The file is a JSON array. The supported kinds are `article`, `book`, `podcast`, `video`, and `other`.

```json
[
  {"kind":"book","title":"Founding Sales","url":"https://www.foundingsales.com/"},
  {"kind":"podcast","title":"Founder-led sales | Pete Kazanjy","url":"https://www.lennysnewsletter.com/p/founder-led-sales-pete-kazanjy-founding"}
]
```

Use `--select 1,2` with a reviewed results file for a repeatable selection. Candidate numbering includes any `--seed` at the start. In the pilot, a reviewed list of real Pete Kazanjy links yielded four selected records, two searchable articles, and one deliberately omitted candidate. Local tests cover the Brave request and response contract. The live provider path requires a configured key.
