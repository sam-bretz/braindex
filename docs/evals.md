# Eval regression suite

Braindex evaluates retrieval and answer generation separately. The retrieval
tests ask which evidence came back. The answer tests ask what a local model did
with that evidence. All cases use synthetic fixtures and disposable libraries.

```sh
sh scripts/eval.sh
sh scripts/eval.sh local
```

The default run includes offline keyword and fixed-vector hybrid retrieval,
topic and access rules, the CLI, MCP and Codex hook context contracts, and
answer fixture and grader checks, and deterministic decision-model tests. The normal `go test ./...` CI run covers the
same offline tests.

The local run adds real hybrid retrieval with `nomic-embed-text` and generated
answers with `qwen2.5:3b`. It also runs the passage ranker comparison and the
inbox suggestion pilot using the same local embedding model. Use an already
running loopback Ollama with cloud
features disabled and both models already installed. The runner sends only
synthetic prompts and passages. It does not install models, discover your
library, or use a hosted model API. Missing models and unavailable local model
services fail the explicit local run. See the [local-only Ollama settings](https://docs.ollama.com/faq#how-do-i-disable-ollamas-cloud-features).

```sh
BRAINDEX_EVAL_MODEL=nomic-embed-text \
BRAINDEX_EVAL_GENERATOR=qwen2.5:3b \
BRAINDEX_OLLAMA_PORT=11434 \
  sh scripts/eval.sh local
```

## What fails the run

- **Retrieval.** Relevant source recall, evidence from multiple selected
  brains, irrelevant results, and queries that should return no evidence.
  Read [the retrieval benchmark](retrieval-benchmark.md) for the two corpora and
  the differences between real model results and fixed vectors.
- **Answer behavior.** A model receives Braindex retrieval output, then returns
  structured evidence and synthesis. The grader checks exact quotes and
  citation identity, separate evidence from each expected brain, and abstention
  when evidence is absent. Adversarial grader fixtures check that forged or
  incomplete evidence fails.
- **Scope and provenance.** Code checks that selected and denied brains behave
  as configured and that the CLI and agent adapters carry the source locator
  and revision. These are mechanical checks.

The answer test exercises generation from supplied context. The live Codex
and Claude Code pilot covered the separate question of whether an agent decides
to call Braindex from project instructions. The local test cannot replace that
pilot. Exact citations also leave the usefulness and factual soundness of the
model's free-text synthesis open for human review.

## Local baseline

Measured on macOS arm64 with Go 1.26.10 and Ollama 0.11.8.

| Fixture and mode | Relevant evidence | Extra results |
| --- | ---: | ---: |
| Source recall, keyword | 5/6 sources | 0 |
| Source recall, local hybrid | 6/6 sources | 4 sources |
| Complementary evidence, keyword and fixed hybrid | 4/4 first-ranked passages within their brains | 0 in each mode |
| Complementary evidence, local hybrid | 4/4 first-ranked passages within their brains | 6 passages, with 1 extra contributing brain |
| Local answer behavior | 6/6 cases, 4 exact citations, 3 abstentions | 0 extra citations |

Both retrieval negatives stayed empty. Keyword missed the credit source on the
downturn paraphrase. The local hybrid gates allow the measured distractors on
their specific cases and fail on additional distractors or lost required
evidence. They allow improvements without changing the fixture. Median query
time in the source-recall fixture was 0.64 ms for keyword and 14.46 ms for
hybrid in this run. The six local answer cases took 7.91 seconds total.

- `nomic-embed-text:latest`, 768 dimensions, minimum cosine similarity 0.55,
  SHA-256 `0a109f422b47e3a30ba2b10eca18548e944e8a23073ee3f3e947efcf3c45e59f`.
- `qwen2.5:3b`, temperature 0 and seed 42,
  SHA-256 `357c53fb659c5076de1d65ccb0b397446227b71a42be9d1603d46168015c9e4b`.

## Local decision-model results

The passage ranker calibrated on five separate synthetic questions. It scored
already-returned hybrid passages within each brain, kept keyword-only evidence,
and used the existing Nomic similarity without another inference call. The
selected cosine cutoff was 0.6703 on this model and calibration set.

| Held-out retrieval fixture | Required evidence before → after | Distractors before → after |
| --- | ---: | ---: |
| Source recall | 6/6 → 6/6 | 4 → 1 |
| Complementary evidence | 4/4 → 4/4 first-ranked within their brains | 6 → 0 |

The two negative queries remained empty. The complementary fixture went from
one irrelevant contributing brain to zero. Across three guarded runs,
incremental scoring p95 was 17–77 µs against a 1 ms pilot budget. The comparison
covers eight synthetic questions with up to three hybrid results per brain.
It measures filtering and reordering those results. The ranker cannot recover
evidence that the candidate search missed, and the pilot leaves production
retrieval unchanged.

The independent [inbox suggestion](inbox-review.md#suggest-a-destination-with-a-local-model)
fixture had two correct suggestions and four abstentions across six selected
items. It checked a weak match, a match only to a denied brain, an ambiguous
match, and a URL. The staged inbox and library were identical before and
after. The cosine floor and minimum gap are pilot thresholds measured with
Nomic and are not calibrated probabilities.

## Compare the ranker on your own questions

Use a current hybrid index built with the calibrated local
`nomic-embed-text:latest` digest from the baseline above. Run a question you
would normally ask and let your topic rules choose the brains.

```sh
braindex context 'For founder-led sales, how do I find my first customers?' --compare-ranker --explain
braindex context 'For founder-led sales, how do I find my first customers?' --compare-ranker --json
```

The top-level result is the current search output. The comparison shows the
same selected brains and the passages the ranker would keep in ranked order.
The text view names kept and omitted citations. In JSON, compare
`ranker_comparison.brains[].excerpts` with the corresponding top-level
`brains[].excerpts`. Use source ID, revision, and locator together to identify
an excerpt. The comparison runs on the passages the current search already
returned, preserves keyword-only citations, and uses no additional inference.
Braindex writes no comparison files and does not change routing or the index.
Unmatched questions, unavailable embeddings, and uncalibrated model digests
return a reason and preserve the baseline.

Try a direct question, a paraphrase, a question that needs two brains, and an
unrelated control. Review the omitted passages for useful evidence even if
the alternative looks cleaner. Pay particular attention to a source that
covers a different part of the question from the strongest match.

In a small public-source CLI pilot using [Founding Sales chapter 4](https://www.foundingsales.com/4-prospecting)
and a local transcript of [Dalton + Michael's fundraising, sales, and hiring episode](https://podcasters.spotify.com/pod/show/daltonplusmichael/episodes/How-Founders-Win-at-Fundraising--Sales--and-Hiring-e3puqem),
both brains contributed before and after on all three scoped questions. The
ranker omitted three of 18 passages. One omitted passage provided relevant
advice about avoiding familiar prospects who lack the target problem. The
ranker also moved an author attribution passage from third to first on one question. The unrelated
control stayed unmatched. These observations support keeping the ranker in
comparison mode while collecting more labeled real queries.

## Add a regression

Add a small synthetic case that reproduces the failure. Label expected
evidence by brain, source, and locator where applicable. Include a plausible
distractor or negative case when it distinguishes the failure. Run offline first
to check the contract, then run locally for changes to ranking, embeddings,
prompts, or generation. Compare the per-case results against the same fixture,
model digest, and runtime. Treat a new model digest as a new baseline. Inspect
both precision and recall before changing any threshold.

## Where fast decision models could help

A System 1 model here means a local classifier or ranker that answers a
bounded question quickly, returns an inspectable score or label, and can
abstain. Compare it against rules first. Keep candidate generation bounded,
and measure latency and false positives as well as the quality gain.

| Area | Small decision to test | Guardrail and comparison |
| --- | --- | --- |
| [Retrieval](../internal/retrieval/context.go) | Score whether a candidate passage contributes evidence for the question. Keep scoring within each selected brain so a second useful perspective can surface. | Compare keyword, current hybrid, and hybrid plus a local ranker on per-brain recall, distractors, and p95 latency. Preserve access and topic rules. |
| [Inbox review](../internal/cli/inbox_handoff.go) | Suggest an existing brain, new-brain review, duplicate review, or skip. Shortlist existing brains using user-written descriptions and a few approved examples. | Measure suggestion precision, abstention, and time to review against current placement. Show suggested destinations with rationale and keep explicit approval. |
| [Author discovery](../internal/connectors/person.go) | Rank whether a byline, podcast guest, or book author matches the intended person. | Compare a metadata-only baseline to a classifier on namesakes, aliases, and reused titles. Require source links and user selection before importing. |
| [Media ingestion](../internal/ingest) and [source refresh](../internal/cli/follow.go) | Decide whether an extraction is usable or worth retrying with OCR, transcription, or manual review. | Use extraction status, text density, and timestamp validity as the baseline. Measure false accepts and unnecessary processing. Keep original locators. |
| [Agent routing](agent-integration.md) | Suggest that an unmatched paraphrase belongs to a configured topic, or that a follow-up still concerns it. | Compare topic-rule matches to labeled paraphrases and unrelated prompts. Surface a suggestion within the user's configured scope. Exact rules and explicit user choices govern retrieval. |
| [Cross-brain context](../internal/retrieval/context.go) | Flag whether separately cited passages reinforce, contrast, or duplicate one another. | Evaluate against human-labeled pairs. Show both excerpts, allow an uncertain label, and keep the synthesis optional. |
| [Package upgrades](../internal/brainpackage/upgrade.go) and [sync](../internal/cli/sync.go) | A classifier could prioritize changes for review after validation. | Keep access checks, checksums, provenance, approval, and retry logic in deterministic code. |

The local runner includes both decision-model pilots. The ranker operates on
the passages already returned by hybrid search and uses a separate calibration
fixture. The inbox pilot uses a different scoring decision and checks explicit
selection, abstention, and library state before and after the command.

A judge is a separate eval tool for subjective criteria such as whether the
free-text synthesis preserves caveats or overstates agreement. Calibrate it
against human labels and keep exact citation and access checks in code.
[MT-Bench judge study](https://arxiv.org/abs/2306.05685)
