package retrieval

import (
	"encoding/json"
	"math"
	"os"
	"reflect"
	"slices"
	"sort"
	"testing"
	"time"

	"github.com/sam-bretz/braindex/internal/index"
	"github.com/sam-bretz/braindex/internal/library"
)

// The experiment only filters and reorders the citations already returned by
// hybrid retrieval, independently within each selected brain. Keyword-only
// citations bypass the semantic filter.
type passageRanker struct {
	lexicalWeight float64
	minimum       float64
}

type rankerFeatures struct {
	cosine, coverage float64
}

func (r passageRanker) score(features rankerFeatures) float64 {
	return features.cosine + r.lexicalWeight*features.coverage
}

func rankerTerms(question string) map[string]bool {
	terms := make(map[string]bool)
	for _, term := range index.Terms(question) {
		terms[term] = true
	}
	return terms
}

func passageFeatures(query map[string]bool, excerpt Excerpt) rankerFeatures {
	features := rankerFeatures{cosine: evalEmbeddingCutoff}
	if excerpt.Similarity != nil {
		features.cosine = *excerpt.Similarity
	}
	for _, term := range index.Terms(excerpt.Text) {
		if query[term] {
			features.coverage++
		}
	}
	features.coverage = min(features.coverage, 3) / 3
	return features
}

func (r passageRanker) apply(query string, result Result) Result {
	terms := rankerTerms(query)
	result.Brains = slices.Clone(result.Brains)
	for i := range result.Brains {
		brain := &result.Brains[i]
		if len(brain.Excerpts) == 0 {
			continue
		}
		type scored struct {
			excerpt Excerpt
			score   float64
		}
		matches := make([]scored, 0, len(brain.Excerpts))
		for _, excerpt := range brain.Excerpts {
			score := r.score(passageFeatures(terms, excerpt))
			if excerpt.Similarity == nil || score >= r.minimum {
				matches = append(matches, scored{excerpt, score})
			}
		}
		sort.SliceStable(matches, func(i, j int) bool { return matches[i].score > matches[j].score })
		brain.Excerpts = make([]Excerpt, 0, len(matches))
		for _, match := range matches {
			brain.Excerpts = append(brain.Excerpts, match.excerpt)
		}
		if len(matches) == 0 {
			brain.Status = "no_relevant_passages"
		}
	}
	return result
}

type rankerLabel struct {
	features rankerFeatures
	relevant bool
}

// Fit a two-feature linear model on calibration only. Each candidate weight
// uses the highest threshold that retains every labeled positive. Prefer the
// fewest accepted distractors and then the widest safe margin.
func fitPassageRanker(labels []rankerLabel) (passageRanker, bool) {
	best := passageRanker{}
	fewest := len(labels) + 1
	bestMargin := -1.0
	for _, weight := range []float64{0, 0.05, 0.10, 0.20} {
		model := passageRanker{lexicalWeight: weight, minimum: math.Inf(1)}
		for _, label := range labels {
			if label.relevant {
				model.minimum = min(model.minimum, model.score(label.features))
			}
		}
		if math.IsInf(model.minimum, 1) {
			return passageRanker{}, false
		}
		falsePositives := 0
		lower := 0.0
		for _, label := range labels {
			if label.relevant {
				continue
			}
			score := model.score(label.features)
			if score >= model.minimum {
				falsePositives++
			} else {
				lower = max(lower, score)
			}
		}
		margin := model.minimum - lower
		if falsePositives < fewest || (falsePositives == fewest && margin > bestMargin) {
			model.minimum = lower + margin/2
			best, fewest, bestMargin = model, falsePositives, margin
		}
	}
	return best, true
}

func TestPassageRankerDecision(t *testing.T) {
	labels := []rankerLabel{
		{rankerFeatures{cosine: 0.64, coverage: 1}, true},
		{rankerFeatures{cosine: 0.68}, false},
	}
	model, ok := fitPassageRanker(labels)
	if !ok || model.lexicalWeight == 0 {
		t.Fatalf("fit did not distinguish lexical evidence from the stronger distractor: %+v", model)
	}
	similarity := 0.64
	citation := Excerpt{SourceID: "good", Locator: "line 7", Revision: "rev", Text: "Air moist plants", Similarity: &similarity}
	stronger := Excerpt{SourceID: "stronger", Locator: "page 4", Revision: "rev2", Text: "Air moist plants", Similarity: new(0.72)}
	distractor := Excerpt{SourceID: "bad", Text: "Cabinet", Similarity: new(0.68)}
	keywordOnly := Excerpt{SourceID: "lexical", Text: "Air moist", Match: "keyword"}
	baseline := Result{Routing: Routing{Scope: "explicit_brains"}, Brains: []BrainResult{
		{ID: "allowed", Status: "hits", Searched: true, Excerpts: []Excerpt{distractor, citation, stronger}},
		{ID: "unrelated", Status: "hits", Searched: true, Excerpts: []Excerpt{distractor}},
		{ID: "keyword-only", Status: "hits", Searched: true, Excerpts: []Excerpt{keywordOnly}},
		{ID: "private", Status: "denied"},
	}}
	got := model.apply("Air moist plants", baseline)
	if !slices.Equal(got.Brains[0].Excerpts, []Excerpt{stronger, citation}) ||
		!slices.Equal(baseline.Brains[0].Excerpts, []Excerpt{distractor, citation, stronger}) {
		t.Fatalf("citations changed or were reordered in the baseline: got %+v", got.Brains)
	}
	if got.Routing != baseline.Routing || got.Brains[1].Status != "no_relevant_passages" || !got.Brains[1].Searched ||
		!slices.Equal(got.Brains[2].Excerpts, []Excerpt{keywordOnly}) ||
		got.Brains[3].Status != "denied" || got.Brains[3].Searched || len(got.Brains[3].Excerpts) != 0 {
		t.Fatalf("ranker changed scope or left a removed hit: %+v", got)
	}
	if _, ok := fitPassageRanker(labels[1:]); ok {
		t.Fatal("fit without positive labels")
	}
}

type rankerFixture struct {
	Sources []struct{ ID, Brain, Title, Locator, Text string }
	Queries []struct {
		ID, Question string
		Brains       []string
		Expected     json.RawMessage
	}
}

type rankerExpected struct {
	brain, locator string
}

type rankerCase struct {
	id, question string
	gold         map[string]rankerExpected
	hybrid       Result
	latency      time.Duration
}

func rankerCases(t *testing.T, path, model string, complementary bool) []rankerCase {
	t.Helper()
	var fixture rankerFixture
	readEvalFixture(t, path, &fixture)
	store, err := library.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	brains := map[string]bool{}
	for _, sample := range fixture.Queries {
		for _, brain := range sample.Brains {
			if !brains[brain] {
				if err := store.CreateBrain(library.Brain{ID: brain}); err != nil {
					t.Fatal(err)
				}
				brains[brain] = true
			}
		}
	}
	sources := map[string]evalSource{}
	for _, source := range fixture.Sources {
		locator := source.Locator
		if locator == "" {
			locator = "line 1"
		}
		sources[source.ID] = putEvalSource(t, store, source.Brain, source.ID, source.Title, locator, source.Text)
	}
	if err := index.Rebuild(store); err != nil {
		t.Fatal(err)
	}
	rebuildEvalEmbeddings(t, store, model)

	cases := make([]rankerCase, 0, len(fixture.Queries))
	for _, sample := range fixture.Queries {
		gold := make(map[string]rankerExpected)
		if complementary {
			var byBrain map[string]struct{ Source, Locator string }
			if err := json.Unmarshal(sample.Expected, &byBrain); err != nil {
				t.Fatal(err)
			}
			for brain, expected := range byBrain {
				if sources[expected.Source].brain != brain {
					t.Fatalf("%s: source %s does not belong to %s", sample.ID, expected.Source, brain)
				}
				gold[expected.Source] = rankerExpected{brain, expected.Locator}
			}
		} else {
			var ids []string
			if err := json.Unmarshal(sample.Expected, &ids); err != nil {
				t.Fatal(err)
			}
			for _, id := range ids {
				source, ok := sources[id]
				if !ok {
					t.Fatalf("%s: expected source %s is missing from the fixture", sample.ID, id)
				}
				gold[id] = rankerExpected{source.brain, "line 1"}
			}
		}
		start := time.Now()
		result, err := ContextWithContext(t.Context(), store, Request{
			Query: sample.Question, Brains: sample.Brains, Limit: 3, CompareRanker: true,
		})
		latency := time.Since(start)
		if err != nil || result.Retrieval != "hybrid" || result.Routing.Scope != "explicit_brains" ||
			len(result.Brains) != len(sample.Brains) || result.RankerComparison == nil {
			t.Fatalf("%s: invalid hybrid comparison: %+v (%v)", sample.ID, result, err)
		}
		for i, brain := range result.Brains {
			if brain.ID != sample.Brains[i] {
				t.Fatalf("%s: selected brain %q, expected %q", sample.ID, brain.ID, sample.Brains[i])
			}
			for _, citation := range brain.Excerpts {
				checkEvalCitation(t, sample.ID, brain.ID, sources[citation.SourceID], citation)
			}
		}
		cases = append(cases, rankerCase{sample.ID, sample.Question, gold, result, latency})
	}
	return cases
}

type rankerQuality struct {
	top1, top3, distractors, extraBrains, nonemptyNegatives int
}

func (q *rankerQuality) observe(result Result, gold map[string]rankerExpected) {
	found := map[string]bool{}
	relevantBrains := map[string]bool{}
	for _, expected := range gold {
		relevantBrains[expected.brain] = true
	}
	for _, brain := range result.Brains {
		for rank, cite := range brain.Excerpts {
			if gold[cite.SourceID] == (rankerExpected{brain.ID, cite.Locator}) {
				if !found[cite.SourceID] {
					q.top3++
					if rank == 0 {
						q.top1++
					}
					found[cite.SourceID] = true
				}
			} else {
				q.distractors++
			}
		}
		if len(brain.Excerpts) > 0 && !relevantBrains[brain.ID] {
			q.extraBrains++
		}
	}
	if len(gold) == 0 && slices.ContainsFunc(result.Brains, func(brain BrainResult) bool { return len(brain.Excerpts) > 0 }) {
		q.nonemptyNegatives++
	}
}

func TestLocalPassageRanker(t *testing.T) {
	modelName := os.Getenv("BRAINDEX_EVAL_MODEL")
	if modelName == "" {
		t.Skip("set BRAINDEX_EVAL_MODEL to a locally installed Ollama embedding model")
	}
	calibration := rankerCases(t, "testdata/ranker-calibration.json", modelName, false)
	var labels []rankerLabel
	positives, keywordOnly := 0, 0
	for _, sample := range calibration {
		terms := rankerTerms(sample.question)
		for _, brain := range sample.hybrid.Brains {
			for _, cite := range brain.Excerpts {
				if cite.Similarity == nil {
					keywordOnly++
					continue
				}
				relevant := sample.gold[cite.SourceID] == (rankerExpected{brain.ID, cite.Locator})
				if relevant {
					positives++
				}
				labels = append(labels, rankerLabel{passageFeatures(terms, cite), relevant})
			}
		}
	}
	model, ok := fitPassageRanker(labels)
	if !ok || positives == len(labels) {
		t.Fatal("need positive and negative hybrid candidates to calibrate")
	}
	t.Logf("calibration: %d positive and %d negative scored candidates, %d keyword-only bypasses, lexical weight %.2f, minimum score %.4f",
		positives, len(labels)-positives, keywordOnly, model.lexicalWeight, model.minimum)

	promote, testedFrozen := true, false
	for _, fixture := range []struct {
		path          string
		complementary bool
	}{
		{"testdata/retrieval-eval.json", false},
		{"testdata/complementary-evidence.json", true},
	} {
		cases := rankerCases(t, fixture.path, modelName, fixture.complementary)
		totalGold := 0
		var baseline, experimental rankerQuality
		var hybridTimes, overheads []time.Duration
		for _, sample := range cases {
			var before, after rankerQuality
			before.observe(sample.hybrid, sample.gold)
			comparison := sample.hybrid.RankerComparison
			var reranked Result
			switch {
			case comparison.Status == "compared":
				testedFrozen = true
				start := time.Now()
				replayed := compareRankerBrains(sample.hybrid.Brains)
				overheads = append(overheads, time.Since(start))
				if !reflect.DeepEqual(replayed, comparison.Brains) {
					t.Fatalf("%s: production comparison differs from the scored candidates", sample.id)
				}
				for _, brain := range comparison.Brains {
					reranked.Brains = append(reranked.Brains, BrainResult{ID: brain.ID, Excerpts: brain.Excerpts})
				}
			case comparison.Reason == "unsupported_model" && comparison.ModelDigest != rankerModelDigest:
				start := time.Now()
				reranked = model.apply(sample.question, sample.hybrid)
				overheads = append(overheads, time.Since(start))
			default:
				t.Fatalf("%s: unexpected comparison outcome: %+v", sample.id, comparison)
			}
			after.observe(reranked, sample.gold)
			baseline.observe(sample.hybrid, sample.gold)
			experimental.observe(reranked, sample.gold)
			hybridTimes = append(hybridTimes, sample.latency)
			totalGold += len(sample.gold)
			if after.top1 < before.top1 || after.top3 < before.top3 || after.nonemptyNegatives > before.nonemptyNegatives {
				promote = false
			}
			t.Logf("%s: rank 1 %d->%d, top 3 %d->%d, distractors %d->%d, extra brains %d->%d",
				sample.id, before.top1, after.top1, before.top3, after.top3,
				before.distractors, after.distractors, before.extraBrains, after.extraBrains)
		}
		slices.Sort(hybridTimes)
		slices.Sort(overheads)
		p95Overhead := overheads[(95*len(overheads)+99)/100-1]
		const budget = time.Millisecond
		if experimental.distractors >= baseline.distractors || p95Overhead > budget {
			promote = false
		}
		t.Logf("%s: rank 1 %d/%d -> %d/%d, top 3 %d/%d -> %d/%d, distractors %d->%d, extra brains %d->%d, nonempty negatives %d->%d",
			fixture.path, baseline.top1, totalGold, experimental.top1, totalGold, baseline.top3, totalGold,
			experimental.top3, totalGold, baseline.distractors, experimental.distractors, baseline.extraBrains,
			experimental.extraBrains, baseline.nonemptyNegatives, experimental.nonemptyNegatives)
		t.Logf("hybrid request median %s, scoring p95 %s (budget %s)", hybridTimes[len(hybridTimes)/2], p95Overhead, budget)
	}
	t.Logf("tested frozen production comparison: %t, meets promotion criteria on both fixtures: %t", testedFrozen, promote)
	if testedFrozen && !promote {
		t.Error("frozen comparison regressed against the fixture criteria")
	}
}
