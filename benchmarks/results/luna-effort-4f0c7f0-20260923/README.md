# Luna effort pilot

This paired, pk-only pilot compares `gpt-6-luna` at low and medium reasoning effort on two coding tasks. Each task used a new session for implementation and the same session for a separate verification/fix pass. Arms alternated order between repetitions, and task order was also swapped. Both arms explicitly selected `--context-policy full`; prompts, system prompt, skills (empty), fixture, and model were otherwise held constant.

All 16 phases completed within the 90-second per-phase and 20-minute whole-run limits. Each arm passed both independent holdout checks for both tasks. Provider usage was present for all responses:

| Arm | Input tokens | of which cached | Uncached input | Output tokens | Responses | Wall time | Holdout checks |
|---|---:|---:|---:|---:|---:|---:|---:|
| Low | 139,214 | 86,528 | 52,686 | 5,780 | 27 | 154.3 s | 4/4 passed |
| Medium | 144,589 | 96,256 | 48,333 | 6,933 | 27 | 182.3 s | 4/4 passed |

Input plus output totals were 144,994 at low and 151,522 at medium, a 4.3% lower aggregate for low. The difference varied by task: low used 66,644 combined tokens on webhook versus 89,584 at medium, while jobqueue used 78,350 at low versus 61,938 at medium. Low also had 9.0% more uncached input in aggregate. These small, stochastic results do not establish a stable cost or quality advantage; there is no pricing calculation here, and provider-reported cached tokens are a subset of input tokens. The production default remains medium.

To reproduce the exact run, create an isolated checkout of base revision `4f0c7f0`, then overlay the manifest-verified files from `source-snapshot/files/` onto that checkout. The snapshot includes the experiment code added on top of the base revision; the bare base commit alone does not support `-effort-ablation`. Run the following from the overlaid checkout, with a configured local pk auth file:

```sh
go run ./cmd/pkbench \
  -repo /path/to/overlaid-benchmark-checkout \
  -effort-ablation -effort-tasks webhook,jobqueue \
  -model gpt-6-luna -repetitions 2 -timeout 90s \
  -out /path/to/new-results-directory
```

The experiment itself applies low and medium to the paired arms; omit `-effort`. Provider calls are bounded by the two fixtures, two repetitions, 90-second phase deadlines, and the experiment's 20-minute total deadline. The run records and generated implementation artifacts are retained beside this report. `source-manifest.json` lists SHA-256 hashes for all 322 files in `source-snapshot/files`; all hashes were verified after the run. `summary.md` was regenerated from the captured `summary.json` after correcting a generic renderer that had described an unrelated output-cap experiment; no provider calls were repeated.
