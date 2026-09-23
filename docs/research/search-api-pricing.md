# Search API price check — September 23, 2026

Primary-source cross-check for the Monid/TinyFish setup example. These are API charges, not the model tokens consumed while using the results. Different result counts, search depth, quotas, and subscription commitments make this an indicative comparison rather than a like-for-like quality benchmark.

| Service / mode | Published cost for 1,000 searches | Conditions |
| --- | --- | --- |
| TinyFish Search | $0 | Free wallet tier, including zero balance; 30 requests/minute. Direct API requires a TinyFish key. |
| Brave Search | $5 | Includes $5 monthly credits. This is Search, not token-billed Answers. |
| Exa Search | $7 | Base price with up to 10 results; deeper search and extra results cost more. |
| Tavily Basic | $8 | PAYG at $0.008/credit, one credit per basic search; 1,000 free credits/month. |
| Tavily Advanced | $16 | PAYG at two credits per search. |
| SerpApi Starter | $25 | $25/month includes 1,000 searches; larger subscriptions change the effective rate. |

Sources: [TinyFish announcement](https://future.tinyfish.io/blog/search-and-fetch-are-now-free-for-every-agent-everywhere), [Brave plans](https://brave.com/search/api/), [Exa pricing](https://exa.ai/pricing), [Tavily credits](https://docs.tavily.com/documentation/api-credits), [SerpApi plans](https://serpapi.com/pricing).

Monid advertises free TinyFish search/fetch and says its example search plus four page fetches cost $0.00. Its public TinyFish catalog presently has no listed endpoints; that does not prove a setup-provisioned or CLI-only route is absent. The explicitly requested Monid CLI setup and route verification are underway. Do not present the primary-source browser checks above as successful Monid calls, and do not substitute a paid catalog tool for the advertised free service.
