#!/bin/sh
set -eu

case "${1:-offline}" in
  offline)
    unset BRAINDEX_EVAL_MODEL BRAINDEX_EVAL_GENERATOR
    ;;
  local)
    export BRAINDEX_EVAL_MODEL="${BRAINDEX_EVAL_MODEL:-nomic-embed-text}"
    export BRAINDEX_EVAL_GENERATOR="${BRAINDEX_EVAL_GENERATOR:-qwen2.5:3b}"
    ;;
  *)
    printf '%s\n' 'Usage: sh scripts/eval.sh [offline|local]' >&2
    exit 2
    ;;
esac
if [ "$#" -gt 1 ]; then
  printf '%s\n' 'Usage: sh scripts/eval.sh [offline|local]' >&2
  exit 2
fi

repo=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
cd "$repo"
# Run one test package at a time so the two local models need not be loaded together.
go test -p 1 -count=1 -v ./internal/retrieval ./internal/eval ./internal/cli ./internal/hook ./internal/mcp \
  -run '^Test(LocalPassageRanker|PassageRankerDecision|RankerComparison|LocalInboxSuggestions|InboxSuggest[^/]*|RetrievalQuality|ComplementaryEvidence|SemanticEvidenceAcrossBrains|ContextUsesUserRulesAndSeparatesOutcomes|ContextExplainKeepsStructuredOutput|ContextExplainShowsKeptAndOmittedCitations|AnswerFixtures|AnswerGrader|LocalAnswerEval|PromptHookOnlyEmitsCitedContextForMatches|ServeReadOnlyContextOverStdioTransport)$'
