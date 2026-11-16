package connectors

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"unicode"
)

const personSearchURL = "https://api.search.brave.com/res/v1/web/search"

type PersonCandidate struct {
	Kind  string `json:"kind"`
	Title string `json:"title"`
	URL   string `json:"url"`
}

var personQueries = []struct {
	kind  string
	terms string
}{
	{"article", "articles essays writing"},
	{"book", "books author publisher"},
	{"podcast", "podcast interview guest"},
	{"video", "YouTube talks lectures"},
}

// SearchPerson searches for candidates in several media categories. Results
// require human review because a web result can mention a different person.
func SearchPerson(ctx context.Context, name, hint, key string) ([]PersonCandidate, error) {
	return searchPerson(ctx, personSearchURL, name, hint, key)
}

func searchPerson(ctx context.Context, endpoint, name, hint, key string) ([]PersonCandidate, error) {
	if strings.TrimSpace(key) == "" {
		return nil, errors.New("set BRAVE_SEARCH_API_KEY or supply --results FILE")
	}
	queryName := `"` + strings.ReplaceAll(name, `"`, " ") + `"`
	var candidates []PersonCandidate
	for _, category := range personQueries {
		query := url.Values{
			"q":                {strings.Join(strings.Fields(queryName+" "+hint+" "+category.terms), " ")},
			"count":            {"8"},
			"result_filter":    {"web"},
			"text_decorations": {"false"},
		}
		data, _, err := get(ctx, endpoint+"?"+query.Encode(), map[string]string{
			"Accept":               "application/json",
			"X-Subscription-Token": key,
		}, 1<<20)
		if err != nil {
			return nil, fmt.Errorf("search for %s results: %w", category.kind, err)
		}
		var response struct {
			Web struct {
				Results []PersonCandidate `json:"results"`
			} `json:"web"`
		}
		if err := json.Unmarshal(data, &response); err != nil {
			return nil, fmt.Errorf("decode %s search results: %w", category.kind, err)
		}
		for _, candidate := range response.Web.Results[:min(8, len(response.Web.Results))] {
			candidate.Kind = category.kind
			candidates = append(candidates, candidate)
		}
	}
	return PersonCandidates(candidates), nil
}

// PersonCandidates validates links and removes repeated results in review order.
func PersonCandidates(input []PersonCandidate) []PersonCandidate {
	candidates := make([]PersonCandidate, 0, len(input))
	seen := make(map[string]bool)
	for _, item := range input {
		address, err := webURL(strings.TrimSpace(item.URL))
		if err != nil || strings.Contains(address.Host, "%") {
			continue
		}
		address.Host = strings.ToLower(address.Host)
		item.URL = address.String()
		if len(item.URL) > 4096 || seen[item.URL] {
			continue
		}
		switch item.Kind {
		case "article", "book", "podcast", "video":
		default:
			item.Kind = "other"
		}
		item.Title = strings.Join(strings.Fields(strings.Map(func(r rune) rune {
			if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
				return ' '
			}
			return r
		}, item.Title)), " ")
		if title := []rune(item.Title); len(title) > 180 {
			item.Title = string(title[:179]) + "…"
		}
		if item.Title == "" {
			item.Title = item.URL
		}
		seen[item.URL] = true
		candidates = append(candidates, item)
	}
	return candidates
}
