# Retrieval benchmark

The [labeled fixture](../internal/retrieval/testdata/retrieval-eval.json) uses eight synthetic documents across four brains and five questions. It tests finding evidence in two different brains, matching direct wording, matching paraphrases, and returning no evidence for an unrelated question.

```sh
go test ./internal/retrieval -run '^TestRetrievalQuality$' -count=1 -v
BRAINDEX_EVAL_MODEL=nomic-embed-text \
  go test ./internal/retrieval -run '^TestRetrievalQuality$' -count=1 -v
```

The first command is offline and runs in CI. The second uses an already running loopback Ollama instance and sends only the synthetic fixture. Results count distinct returned source IDs per question, with up to three excerpts per brain. Recall is relevant source IDs found divided by labeled relevant IDs. Precision is relevant IDs found divided by all IDs returned. Query timing excludes index construction and process startup.

One macOS arm64 run:

| Mode | Recall | Precision | No-evidence case | Median query |
| --- | ---: | ---: | ---: | ---: |
| Keyword | 5/6 | 5/6 | 1/1 | 0.9 ms |
| Hybrid with local `nomic-embed-text` | 6/6 | 6/10 | 1/1 | 20.4 ms |

Hybrid found both sources for the downturn question. It also returned more irrelevant sources across the fixture. These results describe the fixture and model above. Use a larger labeled corpus before drawing conclusions about broader relevance or latency.
