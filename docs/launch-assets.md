# pk launch assets — review drafts

The repository contains three generated brand assets and three UI concept images. Brand art is not a
product screenshot; UI concepts are not captures of the running interface. All are committed for
review, but none has been posted to X or used in an external campaign.

The UI concepts are [dark welcome](../output/imagegen/pk-welcome-dark.png),
[active task](../output/imagegen/pk-active-task.png), and
[light task list](../output/imagegen/pk-tasks-light.png). They are prompts for visual direction, not
screenshots or descriptions of shipped UI.

## Real product screenshot

[pk-coding-session.png](assets/pk-coding-session.png) is a 1920 × 1080 frame from the reviewed local Cmux recording of pk fixing a Go interval-count bug and passing its tests. It now appears in the README. Unlike the concepts above, it shows the running product. The reusable demo fixture and video editor are in `scripts/demo-fixture` and `scripts/demo-edit`.

## Brand card

The first generated brand card is ready for review: [pk-keep-work-moving.png](../output/launch/pk-keep-work-moving.png)
(1672 × 941, approximately 1.7 MiB). It is conceptual campaign art, not a product screenshot. It is
committed in the public repository in `4bf2549`; it has not been posted to X or used in an external
campaign.

Exact generation prompt:

```text
Use case: ads-marketing. Asset type: wide 16:9 launch card for pk, a premium open-source terminal coding agent. Create a refined editorial brand image with charcoal-black background, restrained mint accents, subtle tactile grain and a softly illuminated sculptural lowercase "pk" wordmark inspired by blocky monospace terminal glyphs. Beautiful precise spacing, plenty of negative space, confident minimal composition. Only text, verbatim: "pk" and beneath it in small clean monospace "Keep work moving." A small subtle terminal cursor motif may accompany the wordmark. This is a brand campaign image, not an application screenshot. No fake interface, no benchmark numbers, no other logos, no neon cyberpunk clutter, no watermark.
```

The prompt used the phrase “open-source,” but the public repository has no license selected yet.
Do not repeat open-source or other licensing claims in public copy until the user chooses and adds a
license. Keep these as review drafts; do not use them in social posts or an external promotional
campaign without explicit authorization. The image itself is already part of the public repository.

## Image-generation tool smoke

The image at [pk-mint-cursor.png](../output/launch/pk-mint-cursor.png) was generated during a live pk
smoke through the model-invokable ImageGen tool and visually reviewed. Luna orchestrated one call;
the separately configured image-generation driver was `gpt-6-astra`, authenticated through the
existing ChatGPT login. Generation took about 30 seconds. This does not establish native Luna image
generation or make the image a product screenshot. The ImageGen bridge is committed and installed
in backend checkpoint `1ebe672`.

The root's exact orchestration request was: “Use ImageGen exactly once to create a square minimalist
brand image: a softly illuminated mint terminal cursor on a charcoal background, subtle grain,
generous negative space, no text or logos. Save it as mint-cursor.png. Then report the generated file
path. Do not use Bash or other tools.” The tool's own expanded prompt is unavailable because events
store a bounded preview; the sentence above records the request sent to the agent, not a verbatim
ImageGen tool prompt. The event log is retained locally at `/tmp/pk-imagegen-live-check/events.jsonl`.
This image is a repository review asset, not a social post or external campaign.

## Privacy-forward launch card concept

[pk-your-workspace-your-choice.png](../output/launch/pk-your-workspace-your-choice.png) is a
new 1672 × 941 concept card (about 1.6 MiB). It uses a charcoal field, softly lit mint `pk`
wordmark/cursor, and the line “Your workspace. Your tools. Your choice.” The line is positioning,
not a claim that every provider, MCP server, or extension has the same privacy practices. The image
is brand art, not a product screenshot or evidence of a particular UI state. It is committed for
review and has not been posted or used in an external campaign.

Generation prompt:

```text
Use case: ads-marketing. Asset type: wide 16:9 launch card for the pk terminal coding workspace. Create a premium editorial brand image, not an application screenshot. Charcoal-black matte background, restrained mint light, subtle tactile grain, calm privacy-forward mood, generous negative space. Center a sculptural lowercase “pk” wordmark inspired by precise blocky monospace terminal glyphs, softly lit mint cursor motif beside it. Use refined typography and careful spacing. Text verbatim only: “pk” and beneath it “Your workspace. Your tools. Your choice.” No UI, no fake terminal window, no provider logos, no benchmark numbers, no claims of superiority, no licensing language, no other text, no watermark.
```

Generated once with the built-in image generation tool and inspected after generation. No existing
asset was replaced. This draft has not been posted or used in a campaign.
