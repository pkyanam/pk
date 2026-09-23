package websearch

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/unreallabsai/unreal-agent/harness/tool"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func response(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: &http.Request{}}
}

func TestSearchUsesDocumentedEndpointAndBoundsResults(t *testing.T) {
	var observed bool
	client, err := NewClient(Config{APIKey: "test-key", HTTPClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		observed = req.Method == http.MethodGet && req.URL.Host == "api.search.tinyfish.ai" && req.URL.Query().Get("query") == "fresh news" && req.Header.Get("X-API-Key") == "test-key"
		var results []string
		for i := 0; i < 13; i++ {
			results = append(results, `{"title":"`+strings.Repeat("t", 600)+`","snippet":"`+strings.Repeat("s", 1300)+`","url":"https://example.test"}`)
		}
		return response(200, `{"results":[`+strings.Join(results, ",")+`]}`), nil
	})}})
	if err != nil {
		t.Fatal(err)
	}
	results, err := client.Search(context.Background(), " fresh news ")
	if err != nil {
		t.Fatal(err)
	}
	if !observed {
		t.Fatal("request did not match TinyFish Search contract")
	}
	if len(results) != 10 {
		t.Fatalf("got %d results, want 10", len(results))
	}
	if len(results[0].Title) > 512 || len(results[0].Snippet) > 1200 {
		t.Fatal("search fields exceeded bounds")
	}
}

func TestFetchValidatesURLsAndBoundsAggregateText(t *testing.T) {
	client, err := NewClient(Config{APIKey: "test", HTTPClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodPost || req.URL.Host != "api.fetch.tinyfish.ai" || req.Header.Get("X-API-Key") != "test" {
			t.Fatalf("unexpected request: %s %s", req.Method, req.URL)
		}
		return response(200, `{"results":[{"url":"https://a.test","text":"`+strings.Repeat("a", 20<<10)+`"},{"url":"https://b.test","text":"`+strings.Repeat("b", 20<<10)+`"}]}`), nil
	})}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Fetch(context.Background(), []string{"file:///etc/passwd"}); err == nil {
		t.Fatal("accepted non-http URL")
	}
	results, err := client.Fetch(context.Background(), []string{"https://a.test", "https://b.test"})
	if err != nil {
		t.Fatal(err)
	}
	if len(results.Results) != 2 || len(results.Results[0].Text)+len(results.Results[1].Text) > maxFetchedText {
		t.Fatalf("fetch output exceeded aggregate cap: %#v", results)
	}
	if !results.Results[1].TextTruncated {
		t.Fatal("second page should be marked truncated")
	}
}

func Test429IsActionableAndNoKeyDoesNotEnable(t *testing.T) {
	t.Setenv("TINYFISH_API_KEY", "")
	if _, err := NewClient(Config{}); err == nil || !strings.Contains(err.Error(), "TINYFISH_API_KEY") {
		t.Fatalf("missing-key error=%v", err)
	}
	client, err := NewClient(Config{APIKey: "test", HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return response(429, `{"error":"private"}`), nil })}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Search(context.Background(), "query")
	if err == nil || !strings.Contains(err.Error(), "429") || strings.Contains(err.Error(), "private") {
		t.Fatalf("429 handling error=%v", err)
	}
}

func TestSearchRejectsCrossOriginRedirectWithoutForwardingKey(t *testing.T) {
	var seenSecond bool
	shared := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host == "api.search.tinyfish.ai" {
			return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": []string{"https://attacker.example/steal"}}, Body: io.NopCloser(strings.NewReader("")), Request: req}, nil
		}
		seenSecond = true
		if req.Header.Get("X-API-Key") != "" {
			return nil, errors.New("API key forwarded across origin")
		}
		return response(200, `{"results":[]}`), nil
	})}
	client, err := NewClient(Config{APIKey: "secret", HTTPClient: shared})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Search(context.Background(), "harmless"); err == nil || !strings.Contains(err.Error(), "cross-origin redirects") {
		t.Fatalf("redirect error=%v", err)
	}
	if seenSecond {
		t.Fatal("cross-origin endpoint received a request")
	}
	if shared.CheckRedirect != nil {
		t.Fatal("constructor mutated the caller's shared HTTP client")
	}
}

func TestRegistryAddsOnlyWebToolsWhenKeyExists(t *testing.T) {
	base := tool.NewRegistry(tool.StaticTranslators{}, "Bash")
	decorated := Decorator(Config{})(base)
	if _, ok := decorated.Resolve("WebSearch"); ok {
		t.Fatal("web search available without key")
	}
	t.Setenv("TINYFISH_API_KEY", "test")
	decorated = Decorator(Config{})(base)
	for _, name := range []string{"WebSearch", "WebFetch", "Bash"} {
		if _, ok := decorated.Resolve(name); !ok {
			t.Fatalf("missing registry tool %s", name)
		}
	}
	if len(decorated.StaticDefinitions()) != 3 {
		t.Fatalf("tool definitions=%d, want 3", len(decorated.StaticDefinitions()))
	}
}
