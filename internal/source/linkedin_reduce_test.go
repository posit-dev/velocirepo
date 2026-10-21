package source

import (
	"strings"
	"testing"
)

// LinkedIn's responses are archived with only the fields velocirepo reads,
// re-encoded through the same types the source decodes them with.
func TestReduceLinkedInResponse(t *testing.T) {
	posts := `{"elements":[{"id":"urn:li:share:1","author":"urn:li:organization:9","createdAt":1,"commentary":"hi","visibility":"PUBLIC","lifecycleState":"PUBLISHED","adContext":{"secret":"x"},"distribution":{"targetEntities":[]}}],"paging":{"count":100,"start":0,"total":1}}`
	reduced, ok := reduceLinkedInResponse("https://api.linkedin.com/rest/posts?author=x", []byte(posts))
	if !ok || strings.Contains(string(reduced), "adContext") || strings.Contains(string(reduced), "distribution") || !strings.Contains(string(reduced), "urn:li:share:1") {
		t.Errorf("posts reduced to %s (%v)", reduced, ok)
	}

	stats := `{"elements":[{"share":"urn:li:share:1","totalShareStatistics":{"impressionCount":5,"uniqueImpressionsCount":4,"likeCount":1},"organizationalEntity":"x"}]}`
	reduced, ok = reduceLinkedInResponse("https://api.linkedin.com/rest/organizationalEntityShareStatistics?x", []byte(stats))
	if !ok || strings.Contains(string(reduced), "uniqueImpressionsCount") || !strings.Contains(string(reduced), `"impressionCount":5`) {
		t.Errorf("statistics reduced to %s (%v)", reduced, ok)
	}

	if _, ok := reduceLinkedInResponse("https://api.linkedin.com/rest/somethingElse", []byte(`{}`)); ok {
		t.Error("an unknown endpoint was archived")
	}
	if _, ok := reduceLinkedInResponse("https://api.linkedin.com/rest/posts", []byte(`not json`)); ok {
		t.Error("an unparseable response was archived")
	}
}
