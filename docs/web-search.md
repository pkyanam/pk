# Web search

`WebSearch` returns ranked titles, snippets, and URLs. `WebFetch` returns clean text from up to five selected HTTP(S) URLs. Both use the TinyFish Search and Fetch services through one of two routes: a direct TinyFish API key, or the installed Monid CLI's TinyFish integration.

pk selects a direct key first: `TINYFISH_API_KEY`, then a key saved with `pk web configure`. If neither is present, pk uses Monid only when `monid keys list` reports an active key. The Monid key stays in Monid's own credential store; pk checks only whether a key is active and does not read or copy its value. The Monid adapter uses only TinyFish `/search` and `/fetch`, verifies explicit zero-price metadata before running, and discards results unless Monid reports zero cost and zero billed units. It does not fall back to paid catalog providers when configuration, price verification, or a request fails.

Check or configure the route with:

```sh
pk web status
pk web setup
pk web configure   # reads a direct TinyFish key without echoing it
pk web clear       # removes only the pk-managed direct key
```

`pk web status` names the selected source without exposing credentials. `pk web configure` stores a direct key in `~/.pk/websearch.json` with owner-only permissions; `TINYFISH_API_KEY` takes precedence over that value. To use Monid, configure its CLI separately and verify it with `monid keys list`; `pk web clear` does not remove Monid credentials. Configuration changes apply to newly started sessions; use `/new` to add or refresh the tools in a session.

Search is limited to 10 results and 512-byte queries. Fetch accepts at most five explicit HTTP(S) URLs and returns at most 24 KiB of combined page text. The direct HTTP client times out after 18 seconds; HTTP 429 reports a retry-later error. TinyFish documents free-plan ceilings of 30 Search requests/minute and 150 Fetch URLs/minute. The Monid CLI bridge bounds each command and verifies zero-cost metadata and the final billing report; it intentionally fails closed if those fields are missing or nonzero.

The direct route uses TinyFish's documented [Search API](https://docs.tinyfish.ai/search-api/reference) and [Fetch API](https://docs.tinyfish.ai/fetch-api/reference). TinyFish's [free Search/Fetch announcement](https://future.tinyfish.io/blog/search-and-fetch-are-now-free-for-every-agent-everywhere) says these endpoints do not consume wallet balance, and its [authentication guide](https://docs.tinyfish.ai/authentication) documents API-key authentication. The Monid route is implemented through its installed CLI and active credential state rather than a guessed keyless proxy or paid catalog endpoint.

Live validation of the Go Monid adapter completed one bounded Search call and one bounded Fetch call. Both were accepted only after Monid reported `$0`, zero micro-dollar cost, and `billedUnits: 0`; Search returned 10 results and Fetch returned one page. A separate Monid CLI pricing check searched once and fetched four pricing pages, also reporting `$0` and zero billed units. These are observations from those calls, not a promise about every future request: pk rejects any Monid response that does not explicitly report zero cost. The comparison in [search API pricing](research/search-api-pricing.md) distinguishes TinyFish's free route from paid alternatives.

pk does not operate a first-party telemetry service. Using the Monid route does make explicit network requests through Monid: pk invokes its CLI to check active-key state, inspect TinyFish price metadata, and submit the requested search or fetch. Query text and URLs are passed as command arguments and may be visible briefly to same-user process inspection. Model requests, search/fetch, and other configured integrations contact their respective providers. Search snippets and fetched pages are untrusted web content; verify important claims against linked sources.
