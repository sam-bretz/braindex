# Retrieval benchmark

See the [eval regression suite](evals.md) for the combined runner and the current local-model baseline.

The [labeled fixture](../internal/retrieval/testdata/retrieval-eval.json) uses eight synthetic documents across four brains and five questions. It tests finding evidence in two different brains, matching direct wording, matching paraphrases, and returning no evidence for an unrelated question.

```sh
go test ./internal/retrieval -run '^TestRetrievalQuality$' -count=1 -v
BRAINDEX_EVAL_MODEL=nomic-embed-text \
  go test ./internal/retrieval -run '^TestRetrievalQuality$' -count=1 -v
```

The first command is offline and runs in CI. The second uses an already running loopback Ollama instance and sends only the synthetic fixture. Results count distinct returned source IDs per question, with up to three excerpts per brain. Recall is relevant source IDs found divided by labeled relevant IDs. Precision is relevant IDs found divided by all IDs returned. Query timing excludes index construction and process startup.

Earlier macOS arm64 run before the complementary-evidence ranking change:

| Mode | Recall | Precision | No-evidence case | Median query |
| --- | ---: | ---: | ---: | ---: |
| Keyword | 5/6 | 5/6 | 1/1 | 0.9 ms |
| Hybrid with local `nomic-embed-text` | 6/6 | 6/10 | 1/1 | 20.4 ms |

Hybrid found both sources for the downturn question. It also returned more irrelevant sources across the fixture. These results describe the fixture and model above. Use a larger labeled corpus before drawing conclusions about broader relevance or latency.


## Complementary evidence

The [complementary fixture](../internal/retrieval/testdata/complementary-evidence.json)
adds nine original synthetic passages, five brains, and three queries. It labels
the expected source and locator per brain. It includes same-topic distractors,
an empty brain, and an unrelated query. Hybrid tests use fixed fixture vectors
through Braindex's loopback embedding client, so this runs in CI without a
model download or a paid call.

```sh
go test ./internal/retrieval -run '^TestComplementaryEvidence$' -count=1 -v
```

| Mode | Before top-1 / top-3 coverage | After top-1 / top-3 coverage | Before irrelevant passages | After irrelevant passages |
| --- | --- | --- | ---: | ---: |
| Keyword | 4/4 / 4/4 | 4/4 / 4/4 | 6/10 | 0/4 |
| Hybrid with fixture vectors | 4/4 / 4/4 | 4/4 / 4/4 | 6/10 | 0/4 |

An irrelevant brain contributed in one of five contributing-brain outcomes
before the change and zero of four after. The original keyword benchmark
retained 5/6 recall and improved from 5/6 to 5/5 precision. Its downturn
query still misses one source in keyword mode.

For questions with at least eight distinct search terms and multiple selected
brains, keyword candidates need at least two distinct query terms in the
passage. Braindex favors greater term coverage within each brain and keeps
BM25 order for ties. This reduces results that match only one broad term.
It can also omit a useful passage that uses mostly different wording.
Semantic candidates still use the embedding index's own cutoff, so hybrid
search can recover paraphrases when the model finds them relevant. These
fixture results measure the retrieval behavior, and the fixed vectors do
not measure how well any real embedding model understands the passages.
