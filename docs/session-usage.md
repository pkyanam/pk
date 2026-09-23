# Session token usage

Open `/usage` to inspect provider-reported token totals saved with the current session. Opening the panel reads local session records; it does not request a model response.

The panel shows input, output, cached input, and uncached input tokens, plus the number of recorded model responses. Each metric has its own coverage: a provider may report input and output without reporting cached tokens. Missing data is unavailable, not zero. Uncached input is calculated only for responses with valid input and cached counters.

Totals cover the current session's durable response records. They are not an account-wide billing statement, a dollar estimate, or a measurement of separate child sessions. Reopen the panel to refresh after more responses. Older records may lack the metadata needed to distinguish explicit zero from unreported usage.

This feature is under validation in the development checkout; consult the active `/help` to check whether your installed release includes it.
