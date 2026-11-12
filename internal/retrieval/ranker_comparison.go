package retrieval

import (
	"cmp"
	"slices"

	"github.com/sam-bretz/braindex/internal/index"
)

// These settings were calibrated on the local Nomic model. A replacement
// model needs a new calibration before its scores can be compared.
const (
	rankerModelDigest   = "0a109f422b47e3a30ba2b10eca18548e944e8a23073ee3f3e947efcf3c45e59f"
	rankerMinimumCosine = 0.6703
)

// RankerComparison reports what a calibrated filter and rerank would return
// from the baseline excerpts already selected by hybrid search.
type RankerComparison struct {
	Status        string                  `json:"status"`
	Reason        string                  `json:"reason,omitempty"`
	Model         string                  `json:"model,omitempty"`
	ModelDigest   string                  `json:"model_digest,omitempty"`
	MinimumCosine float64                 `json:"minimum_cosine,omitempty"`
	Brains        []RankerBrainComparison `json:"brains,omitempty"`
}

type RankerBrainComparison struct {
	ID       string    `json:"id"`
	Status   string    `json:"status"`
	Excerpts []Excerpt `json:"excerpts"`
	Removed  int       `json:"removed"`
}

func withRankerComparison(result Result, request Request, semantic *index.SemanticIndex) Result {
	if !request.CompareRanker {
		return result
	}
	comparison := &RankerComparison{Status: "unavailable"}
	result.RankerComparison = comparison
	switch {
	case result.Status == "unmatched":
		comparison.Reason = "unmatched"
	case request.Mode == "keyword":
		comparison.Reason = "keyword_mode"
	case request.Mode == "semantic":
		comparison.Reason = "semantic_mode"
	case result.EmbeddingStatus == "stale":
		comparison.Reason = "embeddings_stale"
	case result.EmbeddingStatus == "unavailable":
		comparison.Reason = "embeddings_unavailable"
	case result.Retrieval == "hybrid" && semantic != nil:
		comparison.Model, comparison.ModelDigest = semantic.ModelIdentity()
		if comparison.ModelDigest != rankerModelDigest {
			comparison.Reason = "unsupported_model"
			break
		}
		comparison.Status = "compared"
		comparison.MinimumCosine = rankerMinimumCosine
		comparison.Brains = compareRankerBrains(result.Brains)
	case slices.ContainsFunc(result.Brains, func(brain BrainResult) bool { return brain.Searched }):
		comparison.Reason = "embeddings_missing"
	default:
		comparison.Reason = "no_searchable_brains"
	}
	return result
}

func compareRankerBrains(brains []BrainResult) []RankerBrainComparison {
	comparisons := make([]RankerBrainComparison, 0, len(brains))
	for _, brain := range brains {
		alternative := RankerBrainComparison{
			ID: brain.ID, Status: brain.Status, Excerpts: make([]Excerpt, 0, len(brain.Excerpts)),
		}
		for _, excerpt := range brain.Excerpts {
			if excerpt.Similarity != nil {
				score := *excerpt.Similarity
				if score < rankerMinimumCosine {
					continue
				}
				excerpt.Similarity = &score
			}
			alternative.Excerpts = append(alternative.Excerpts, excerpt)
		}
		slices.SortStableFunc(alternative.Excerpts, func(left, right Excerpt) int {
			return cmp.Compare(rankerSimilarity(right), rankerSimilarity(left))
		})
		alternative.Removed = len(brain.Excerpts) - len(alternative.Excerpts)
		if brain.Status == "hits" && len(alternative.Excerpts) == 0 {
			alternative.Status = "no_relevant_passages"
		}
		comparisons = append(comparisons, alternative)
	}
	return comparisons
}

func rankerSimilarity(excerpt Excerpt) float64 {
	if excerpt.Similarity == nil {
		return 0
	}
	return *excerpt.Similarity
}
