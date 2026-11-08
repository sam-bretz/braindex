package connectors

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSearchPersonUsesMediaQueriesAndReturnsValidCandidates(t *testing.T) {
	requests := make(chan string, len(personQueries))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.Header.Get("X-Subscription-Token") != "fixture-token" || q.Get("count") != "8" ||
			!strings.HasPrefix(q.Get("q"), `"Sample Author" robotics `) {
			t.Error("unexpected search request")
			http.Error(w, "bad query", http.StatusBadRequest)
			return
		}
		requests <- q.Get("q")
		fmt.Fprint(w, `{"web":{"results":[{"title":"A\u001b\u202e result","url":"https://example.org/page#chapter"},{"title":"Ignore","url":"file:///tmp/private"}]}}`)
	}))
	defer server.Close()
	candidates, err := searchPerson(t.Context(), server.URL, "Sample Author", "robotics", "fixture-token")
	if err != nil {
		t.Fatal(err)
	}
	close(requests)
	count := 0
	for query := range requests {
		if !strings.HasSuffix(query, personQueries[count].terms) {
			t.Errorf("unexpected query: %s", query)
		}
		count++
	}
	if count != 4 || len(candidates) != 1 || candidates[0] != (PersonCandidate{"article", "A result", "https://example.org/page"}) {
		t.Fatalf("queries = %d, candidates = %+v", count, candidates)
	}
}
