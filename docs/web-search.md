# Web search

In source revisions containing this integration, `TINYFISH_API_KEY` enables two model tools: `WebSearch` (ranked titles, snippets, and URLs) and `WebFetch` (clean text from selected HTTP(S) URLs). The tools use TinyFish's Search and Fetch REST endpoints, which TinyFish says do not consume wallet balance. They still require a TinyFish account/API key; the Monid page's no-key headline does not match its own skill/API docs. pk does not call paid Monid catalog endpoints. This integration is not in installed release `eef6c46` yet.

Create a TinyFish API key, then export it before starting pk:

```sh
export TINYFISH_API_KEY='...'
pk
```

The key is sent only in the `X-API-Key` header and is never included in tool output. The tools are omitted when the environment variable is unset. Search is limited to 10 results and 512-byte queries. Fetch accepts at most five explicit HTTP(S) URLs and returns at most 24 KiB of combined page text. The default HTTP client times out after 18 seconds; HTTP 429 reports a retry-later error. TinyFish documents free-plan ceilings of 30 Search requests/minute and 150 Fetch URLs/minute.

TinyFish documents Search at [`GET api.search.tinyfish.ai`](https://docs.tinyfish.ai/search-api/reference) and Fetch at [`POST api.fetch.tinyfish.ai`](https://docs.tinyfish.ai/fetch-api/reference). Their [free Search/Fetch announcement](https://future.tinyfish.io/blog/search-and-fetch-are-now-free-for-every-agent-everywhere) says no credit card is required, while their [authentication guide](https://docs.tinyfish.ai/authentication) requires an API key. Monid's [TinyFish announcement](https://monid.ai/blog/tinyfish) describes the offer as keyless, but the linked [Monid skill](https://monid.ai/SKILL.md) says Monid API endpoints require a key and its [public Tinyfish catalog page](https://monid.ai/tools/tinyfish) currently lists no endpoints. Accordingly, pk uses the documented direct TinyFish API with an opt-in key rather than relying on an undocumented keyless proxy.

One harmless unauthenticated Search request during implementation returned HTTP 401 `MISSING_API_KEY`; no `TINYFISH_API_KEY` was configured locally, so an authenticated live query was not run. Client behavior is covered by local HTTP fixtures. Search snippets and fetched pages are untrusted web content; verify important claims against linked sources.
