// Package retrieval applies user-owned topic rules to cited library search.
package retrieval

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sam-bretz/braindex/internal/index"
	"github.com/sam-bretz/braindex/internal/library"
)

const (
	defaultLimit = 3
	maxLimit     = 10
	excerptRunes = 600
)

type Request struct {
	Query     string
	Topic     string
	Brains    []string
	Limit     int
	AllBrains bool
	Mode      string
}

type Result struct {
	Retrieval       string        `json:"retrieval,omitempty"`
	EmbeddingStatus string        `json:"embedding_status,omitempty"`
	Status          string        `json:"status"`
	Topics          []string      `json:"topics"`
	Routing         Routing       `json:"routing"`
	Brains          []BrainResult `json:"brains"`
}

type Routing struct {
	Scope string `json:"scope"`
}

type BrainResult struct {
	ID                 string    `json:"id"`
	Status             string    `json:"status"`
	Searched           bool      `json:"searched"`
	Explicit           bool      `json:"explicit,omitempty"`
	SelectedByTopics   []string  `json:"selected_by_topics,omitempty"`
	Excerpts           []Excerpt `json:"excerpts"`
	UnavailableSources []string  `json:"unavailable_sources,omitempty"`
}

type Excerpt struct {
	Match      string   `json:"match,omitempty"`
	Similarity *float64 `json:"similarity,omitempty"`
	SourceID   string   `json:"source_id"`
	Revision   string   `json:"revision"`
	Title      string   `json:"title"`
	Author     string   `json:"author,omitempty"`
	Date       string   `json:"date,omitempty"`
	URL        string   `json:"url,omitempty"`
	Locator    string   `json:"locator"`
	Text       string   `json:"text"`
	Truncated  bool     `json:"truncated,omitempty"`
}

func Context(store *library.Store, request Request) (Result, error) {
	return ContextWithContext(context.Background(), store, request)
}

func ContextWithContext(ctx context.Context, store *library.Store, request Request) (Result, error) {
	if request.Mode == "" {
		request.Mode = "auto"
	}
	if request.Mode != "auto" && request.Mode != "keyword" && request.Mode != "semantic" {
		return Result{}, errors.New("retrieval mode must be auto, keyword, or semantic")
	}
	if request.AllBrains && (request.Topic != "" || len(request.Brains) > 0) {
		return Result{}, errors.New("all-brains scope cannot be combined with a topic or explicit brain IDs")
	}
	if strings.TrimSpace(request.Query) == "" {
		return Result{}, errors.New("context query is required")
	}
	if request.Limit < 0 || request.Limit > maxLimit {
		return Result{}, fmt.Errorf("limit must be between 1 and %d", maxLimit)
	}
	if request.Limit == 0 {
		request.Limit = defaultLimit
	}
	rules, err := Rules(store)
	if err != nil {
		return Result{}, err
	}
	topics, selected, err := selectBrains(rules, request)
	if err != nil {
		return Result{}, err
	}
	result := Result{Status: "unmatched", Topics: topics, Brains: []BrainResult{}}
	switch {
	case request.AllBrains:
		result.Routing.Scope = "all_brains"
	case request.Topic != "":
		result.Routing.Scope = "explicit_topic"
	case len(request.Brains) > 0:
		result.Routing.Scope = "explicit_brains"
	case len(topics) > 0:
		result.Routing.Scope = "matched_rules"
	default:
		result.Routing.Scope = "unmatched"
	}
	topicSelections := make(map[string][]string)
	if !request.AllBrains {
		for _, rule := range rules {
			if !slices.Contains(topics, rule.ID) {
				continue
			}
			for _, brainID := range unique(rule.Brains) {
				topicSelections[brainID] = append(topicSelections[brainID], rule.ID)
			}
		}
	}
	if len(selected) == 0 && !request.AllBrains {
		return result, nil
	}
	result.Status = "matched"
	result.Retrieval = "keyword"

	snapshot, err := store.Snapshot()
	if err != nil {
		return Result{}, err
	}
	brains := make(map[string]library.Brain, len(snapshot.Brains))
	if request.AllBrains {
		selected = nil
	}
	for _, brain := range snapshot.Brains {
		brains[brain.ID] = brain
		if request.AllBrains {
			selected = append(selected, brain.ID)
		}
	}
	sources := make(map[string]library.Source, len(snapshot.Sources))
	for _, source := range snapshot.Sources {
		sources[source.ID] = source
	}

	if len(selected) == 0 {
		result.Status, result.Retrieval = "unmatched", ""
		return result, nil
	}
	var searchIndex *index.Index
	var semantic *index.SemanticIndex
	semanticAttempted := false
	terms := index.Terms(request.Query)
	// Longer questions that span several brains often contain broad words that
	// retrieve a passage from an otherwise irrelevant brain. Let each brain
	// contribute evidence for its part of the question without padding results.
	var selectiveTerms map[string]bool
	if len(selected) > 1 && len(terms) >= 8 {
		selectiveTerms = make(map[string]bool, len(terms))
		for _, term := range terms {
			selectiveTerms[term] = true
		}
	}
	for _, id := range selected {
		brain, exists := brains[id]
		if !exists {
			return Result{}, fmt.Errorf("unknown brain %q", id)
		}
		access, err := allowed(store, id)
		if err != nil {
			return Result{}, err
		}
		outcome := BrainResult{
			ID: id, Excerpts: []Excerpt{}, Explicit: slices.Contains(request.Brains, id),
			SelectedByTopics: topicSelections[id],
		}
		if !access {
			outcome.Status = "denied"
			result.Brains = append(result.Brains, outcome)
			continue
		}

		var searchable []string
		for _, sourceID := range brain.Sources {
			if hasPassages(sources[sourceID]) {
				searchable = append(searchable, sourceID)
			} else {
				outcome.UnavailableSources = append(outcome.UnavailableSources, sourceID)
			}
		}
		if len(searchable) == 0 {
			outcome.Status = "unavailable"
			result.Brains = append(result.Brains, outcome)
			continue
		}
		if request.Mode != "keyword" && !semanticAttempted {
			semanticAttempted = true
			queryCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
			semantic, err = index.OpenSemantic(queryCtx, store, snapshot.Sources, request.Query)
			cancel()
			switch {
			case err == nil:
				defer semantic.Close()
				result.Retrieval = "hybrid"
				if request.Mode == "semantic" {
					result.Retrieval = "semantic"
				}
			case request.Mode == "semantic":
				return Result{}, err
			case errors.Is(err, index.ErrEmbeddingsStale):
				result.EmbeddingStatus = "stale"
			case !errors.Is(err, index.ErrEmbeddingsMissing):
				result.EmbeddingStatus = "unavailable"
			}
		}

		candidates := request.Limit
		if semantic != nil || len(selectiveTerms) > 0 {
			candidates *= 3
		}
		var keyword []index.Match
		if request.Mode != "semantic" {
			if searchIndex == nil {
				searchIndex, err = index.Open(store, snapshot.Sources)
				if err != nil {
					return Result{}, err
				}
				defer searchIndex.Close()
			}
			keyword, err = searchIndex.Search(request.Query, searchable, candidates)
			if err != nil {
				return Result{}, err
			}
			if len(selectiveTerms) > 0 {
				keyword = rankKeywordEvidence(keyword, selectiveTerms)
			}
		}
		var vectors []index.VectorMatch
		if semantic != nil {
			vectors, err = semantic.Search(ctx, searchable, candidates)
			if err != nil {
				return Result{}, err
			}
		}
		outcome.Searched = true
		outcome.Status = "no_relevant_passages"
		for _, match := range combineMatches(keyword, vectors, request.Limit) {
			outcome.Status = "hits"
			source := sources[match.SourceID]
			text, truncated := excerpt(match.Text, terms)
			outcome.Excerpts = append(outcome.Excerpts, Excerpt{
				SourceID: match.SourceID, Revision: match.Revision, Title: source.Title,
				Author: source.Author, Date: source.Date, URL: source.URL, Locator: match.Locator,
				Text: text, Truncated: truncated, Match: match.kind, Similarity: match.similarity,
			})
		}
		result.Brains = append(result.Brains, outcome)
	}
	return result, nil
}

// rankKeywordEvidence favors coverage of distinct query terms, retaining
// BM25 order for ties. Semantic candidates independently meet the model cutoff.
func rankKeywordEvidence(matches []index.Match, queryTerms map[string]bool) []index.Match {
	type evidence struct {
		match    index.Match
		coverage int
	}
	ranked := make([]evidence, 0, len(matches))
	for _, match := range matches {
		covered := make(map[string]bool)
		for _, term := range strings.Fields(normalize(match.Text)) {
			if queryTerms[term] {
				covered[term] = true
			}
		}
		if len(covered) >= 2 {
			ranked = append(ranked, evidence{match: match, coverage: len(covered)})
		}
	}
	sort.SliceStable(ranked, func(i, j int) bool { return ranked[i].coverage > ranked[j].coverage })
	result := matches[:0]
	for _, item := range ranked {
		result = append(result, item.match)
	}
	return result
}

type rankedMatch struct {
	index.Match
	kind       string
	similarity *float64
	score      float64
}

func combineMatches(keyword []index.Match, semantic []index.VectorMatch, limit int) []rankedMatch {
	const rankOffset = 60
	type key struct{ source, revision, locator string }
	ranks := map[key]int{}
	matches := make([]rankedMatch, 0, len(keyword)+len(semantic))
	for i, item := range keyword {
		id := key{item.SourceID, item.Revision, item.Locator}
		if _, seen := ranks[id]; seen {
			continue
		}
		ranks[id] = len(matches)
		matches = append(matches, rankedMatch{Match: item, kind: "keyword", score: 1 / float64(rankOffset+i+1)})
	}
	for i, item := range semantic {
		id := key{item.SourceID, item.Revision, item.Locator}
		score := 1 / float64(rankOffset+i+1)
		similarity := item.Similarity
		if at, found := ranks[id]; found {
			match := &matches[at]
			if match.similarity == nil {
				match.score += score
				match.kind, match.similarity = "both", &similarity
			}
			continue
		}
		ranks[id] = len(matches)
		matches = append(matches, rankedMatch{Match: item.Match, kind: "semantic", similarity: &similarity, score: score})
	}
	sort.SliceStable(matches, func(i, j int) bool { return matches[i].score > matches[j].score })
	return matches[:min(limit, len(matches))]
}

func hasPassages(source library.Source) bool {
	for _, passage := range source.Passages {
		if strings.TrimSpace(passage.Text) != "" {
			return true
		}
	}
	return false
}

func excerpt(text string, terms []string) (string, bool) {
	if utf8.RuneCountInString(text) <= excerptRunes {
		return text, false
	}
	runes := []rune(text)
	lower := strings.ToLower(text)
	first := len(lower)
	for _, term := range terms {
		if offset := strings.Index(lower, term); offset >= 0 && offset < first {
			first = offset
		}
	}
	start := 0
	if first < len(lower) {
		start = max(0, utf8.RuneCountInString(lower[:first])-excerptRunes/3)
	}
	start = min(start, len(runes)-excerptRunes)
	return string(runes[start : start+excerptRunes]), true
}
