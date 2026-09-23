# Token usage and request size

The current `/usage` view reads provider-reported totals for the saved session. It does not make a
model request. Input, output, cached-input, and derived uncached-input totals are accumulated across
recorded responses; each row shows how many responses contributed a valid value. An unavailable
counter is shown as unavailable, not as zero. These are session totals, not a measure of current
context occupancy.

Starting with v0.1.3, `/usage` adds a composition view for the most recent provider request and footer counters
for that response. The footer's input, output, and cached values are the latest response's
provider-reported counters, not totals for the session. Cached input is reported separately as a
subset of input where the provider supplies it. Provider schemas differ, so coverage can be partial.

Composition is reported as JSON-value bytes and item counts for the system prompt, tool schemas,
non-system messages, tool calls, tool results, and other input. The total includes JSON-encoded input
values and tool-schema values. Image payloads contribute their encoded bytes. These measurements
describe the harness request values: they are not token counts, complete provider wire sizes, or
predictions of how a provider tokenizer will count the request. There is no validated context-window
capacity estimate or category-by-category token estimate.

The latest request can be marked pending while the provider is responding; until that response
finishes, latest-response usage counters are unavailable rather than copied from the previous
request. Context measurements store numeric metadata only, not prompt or tool contents. The
provider-reported totals remain separate from byte composition, and neither is a cost estimate.


Older sessions without a saved composition snapshot still show their recorded token totals.
