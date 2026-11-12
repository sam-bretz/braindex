# Paste a series link and cite a selected video

Install yt-dlp and start from a new Braindex library. Preview the Dalton + Michael channel, then create a brain by selecting three videos.

```sh
library="$(mktemp -d)/library"
channel="https://www.youtube.com/@daltonplusmichael/videos"
braindex --library "$library" init
braindex --library "$library" link "$channel" --json
braindex --library "$library" link "$channel" --new-brain dalton-michael
```

At the prompt, enter three comma-separated item numbers. Save the three source IDs printed by the command. Add the channel as a follow and refresh it.

```sh
braindex --library "$library" follow add channel "$channel" \
  --brain dalton-michael --kind youtube --yt-dlp
braindex --library "$library" follow refresh channel
```

Refresh can add metadata for more videos. Process only the three source IDs you chose. Replace the example IDs below with the saved IDs.

```sh
braindex --library "$library" follow process channel SOURCE_ID_1 SOURCE_ID_2 SOURCE_ID_3
braindex --library "$library" context "What advice do the hosts give about customer discovery?" \
  --brain dalton-michael --json
```

The processing results identify which captions became searchable. A matching
excerpt in the context result includes the video URL, revision, and timestamp.
Choose a question that matches the videos you processed. A repeat of the same
processing command reports cached captions when the tracks are available locally.

The offline acceptance test exercises the full command sequence with a local
yt-dlp substitute and four synthetic videos. It verifies that preview and
refresh perform metadata discovery, only the three selected caption tracks
are fetched, and the repeated processing run uses their cached revisions.
Its question about pilot budgets cites the synthetic video at
`00:02:10.000–00:02:28.000`. The test makes no requests to YouTube.

A separate live smoke processed one video from the public channel. The query
about investor introductions returned a cited passage at
`00:09:55.777–00:10:20.768`, and a repeat used the cached caption track.
[Video used in the live smoke](https://youtu.be/fLSitI_NSUE)

```sh
go test ./internal/cli -run '^TestLinkFollowFromSeriesToSelectedCitations$' -count=1
```

## Save a PDF link

A PDF preview identifies the document. Choose a brain or the inbox to save
its URL as a reference. Braindex does not run the PDF parser on a remote link.
To extract page-cited text from a trusted local file, use
`braindex source add document.pdf --brain ID` and rebuild the index.
Local extraction limits file size, page count, and cumulative extracted text.
The parser can still expand internal streams before those limits take effect.

## Follow a feed or author

Use `follow add ID URL --brain BRAIN` for RSS and Atom. Add `--kind podcast` for an audio feed. `follow list` shows saved subscriptions. `follow refresh ID` reports each item's change and search readiness. Public refreshes reject private network destinations, including redirects. Use `--allow-private` only when adding a feed you trust on your local network. The yt-dlp refresh path reads up to 50 videos per run.

To review an author without publishing the search to a provider:

```sh
braindex follow add pete --author "Pete Kazanjy" --hint "Founding Sales" --brain sales
braindex follow refresh pete --results reviewed-results.json
braindex follow save pete CANDIDATE_ID
```

A results file uses the [author discovery format](media-pilot.md#discover-and-review-work-by-a-person). Author refreshes stage candidates for review. Only the IDs you select with `follow save` join the brain, as references. Pass `--search` explicitly to refresh an author through the configured search provider.
