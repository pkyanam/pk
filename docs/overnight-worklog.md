# Overnight pk improvement log

This log is the handoff for the scheduled improvement passes through 08:00 EDT on 23 September
2026. Each pass should append a dated checkpoint with completed work, evidence, blockers, and the
next small step. Keep changes reviewable; do not publish marketing material or claim benchmark gains
without evidence.

## Scope and order

1. Finish and validate the foreground `AskUser` flow and terminal scrolling/layout fixes. Keep
   unattended task workers free of model-controlled blocking questions.
2. Polish the OpenTUI experience, including keyboard and mouse interaction, using real terminal
   checks at narrow and ordinary sizes.
3. Research support for files, PDFs, and images against the actual provider/model APIs; implement
   only supported behavior and document limits.
4. Build a reproducible success, latency, token, and cache measurement before making optimization
   claims. The cache-runtime owner is responsible for this benchmark work.
5. Keep the six-harness comparison in the research owner's document. After technical behavior is
   validated, draft optional image-generation and X assets for review; do not publish them.

Each change should preserve workspace and process permissions as currently documented. A new
interaction must not imply that Bash is sandboxed. Avoid broad rewrites when a small tested change
will do.

## Checkpoints

### 22 September 2026 — initial overnight handoff

- `fd9633c` added the concise README and a design preview. The preview is conceptual, not a product
  screenshot.
- `4f0a57e` shipped the OpenTUI agent, durable tasks, cache telemetry, and rich activity. The parent
  reports GitHub Actions green for this checkpoint.
- The parent reports the rich-progress implementation passed the full Go race suite and the OpenTUI
  suite; an installed 120×36 PTY showed Bash command/output/exit information, a Luna progress message,
  a second tool result, and the final answer in order.
- The foreground question broker and question UI are in the working tree. The UI supports listed
  choices or freeform composer input, waits for an answer acknowledgment, and lets Escape cancel the
  current question/turn. Root's live validation is still pending; keep public docs marked as planned
  until that evidence is recorded.
- Next: finish live question and scrolling checks, then update the getting-started, architecture,
  research, and validation docs to distinguish foreground questions from detached tasks. Continue
  with UX polish, supported file/PDF/image research, then reproducible benchmark work.

## Checkpoint template

```text
### YYYY-MM-DD HH:MM EDT — short label

- Changed:
- Evidence: commands/tests/smokes and outcomes
- Open issues or limits:
- Next:
```
