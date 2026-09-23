# pk launch video: capture and edit plan

Preflight checked 2026-09-22. This is a local, real-product demo plan; it does not authorize upload or publication. Use a fresh task workspace and show only behavior that works in the build recorded. Keep the finished piece between 30 and 60 seconds, 1080p/30 fps, and avoid claims about speed, model quality, or unattended completion that the recording does not establish.

## Local tools verified

- Cmux is installed at `/Applications/cmux.app`; its CLI is `/Applications/cmux.app/Contents/Resources/bin/cmux` (version 0.64.25). The CLI accepts a directory as its argument and opens it in a workspace. Root should open/focus Cmux through Computer Use first, then use that CLI to open the demo checkout.
- Cap is installed at `/Applications/Cap.app`; its CLI is `/Applications/Cap.app/Contents/MacOS/cap-cli` (CLI 0.1.0, bundled with Cap Desktop 0.6.0). The shell shim at `~/.cap/bin/cap` is not on PATH. `cap doctor --json` reported capture ready, screen permission granted, and ffmpeg available. No recording was started during preflight.
- FFmpeg and ffprobe are available at `/opt/homebrew/bin/ffmpeg` and `/opt/homebrew/bin/ffprobe`.

Relevant help checked from the installed Cap CLI: `record start` accepts `--window` or `--screen`, `--mode studio|instant`, `--fps`, `--duration`, `--path`, `--detach`, and `--json`. `export` accepts MP4, output resolution/fps, compression quality, and filesize optimization. `project validate` is available. A `.cap` project path must not already exist, even as an empty directory.

## Capture sequence

1. Create a clean, non-sensitive demo checkout/workspace and verify `pk` starts there. Use Cmux's directory-open command: `/Applications/cmux.app/Contents/Resources/bin/cmux /path/to/demo-checkout`. Do not record setup, login, or unrelated desktop windows.
2. Run Cap's read-only checks and identify the Cmux window dynamically. Keep IDs out of the script and retrieve the current matching Cmux window at recording time; if the result is ambiguous, stop and select the intended window explicitly rather than recording the whole display.
3. Start a window-scoped Studio capture, with no camera or microphone:

   ```sh
   CAP=/Applications/Cap.app/Contents/MacOS/cap-cli
   OUT="$HOME/Movies/pk-launch-demo"
   mkdir -p "$OUT"
   "$CAP" doctor --json
   "$CAP" targets --json
   # Set WINDOW_ID from the intended Cmux window in the targets output.
   "$CAP" record start --window "$WINDOW_ID" --mode studio --fps 30 \
     --duration 50 --path "$OUT/pk-launch-demo.cap" --detach --json
   ```

   Use Cap's returned recording ID to stop early if needed: `"$CAP" record stop --id "$RECORDING_ID" --json`. Then validate and export:

   ```sh
   "$CAP" project validate "$OUT/pk-launch-demo.cap" --json
   "$CAP" export "$OUT/pk-launch-demo.cap" --output "$OUT/pk-launch-demo.mp4" \
     --fps 30 --resolution 1920x1080 --quality social --optimize-filesize --json
   /opt/homebrew/bin/ffprobe -v error -show_format -show_streams -of json \
     "$OUT/pk-launch-demo.mp4"
   ```

   Check the actual `targets --json` shape and current Cap help before scripting ID extraction; never hard-code a window ID. Keep the `.cap` project beside the MP4 so edits remain possible.

## 30–45 second storyboard: inclusive-range fix

Use the deterministic disposable Go fixture in `scripts/demo-fixture`; it starts with a genuine
failing test for an off-by-one bug in closed integer intervals. The demo shows one foreground
session in that fixture, not a detached task, subagent, or steering flow. Create it before recording:

```sh
WORKSPACE=$(scripts/demo-fixture/create.sh | sed -n 's/^Created disposable coding fixture: //p')
cd "$WORKSPACE"
pk
```

In the live session, submit a request such as:

> Fix the inclusive range endpoint count in `intervals/coverage.go`, keep overlap handling correct, add or adjust tests if needed, run `go test ./...`, and verify the README CLI example produces `{"covered":5}`. Explain the change briefly.

Let the real run determine the visible progress and timing. The fixture’s independent verification,
after the pk response, is:

```sh
cd "$WORKSPACE"
go test ./...
printf '{"ranges":[{"start":1,"end":3},{"start":5,"end":6}]}\n' | go run ./cmd/covercount
```

Expected CLI output after a correct fix: `{"covered":5}`. `scripts/demo-fixture/reset.sh "$WORKSPACE"`
removes only an unmodified, marker-verified fixture; do not use it if the agent changed the directory.

| Time | Picture / action | On-screen wording |
|---|---|---|
| 0–3s | Title overlay over the real terminal | “pk · Go + OpenTUI” |
| 3–9s | Show the disposable workspace and submit the interval-count request | “A real bug, in a disposable Go project” |
| 9–25s | Keep the single continuous run legible as assistant updates and tool results appear | No speed or token-savings claim; retain truthful progress and test output |
| 25–36s | Show the final response and the run’s actual test result, if completed | “Closed intervals count both endpoints” only if the code/result shows it |
| 36–42s | Independently run the tests and README CLI example in the same workspace | Show actual `go test` result and `{"covered":5}` |
| 42–45s | End card | “pk · Coding work, in your workspace” |

The times are an edit target, not a promise that the run finishes in 45 seconds. Record the full
real sequence, then choose a continuous 30–45 second segment that contains only events that actually
happened. If the coding run takes longer or the result is not correct, show truthful progress or
retake after fixing the fixture; never splice independent runs to imply one continuous success.
Keep the independent verification in the cut only when it follows the shown coding run in the same
workspace. The existing FFmpeg script adds only opening, lower-third, and outro overlays; it does not
join multiple runs.

## Edit and delivery

Use Cap Studio's local capture/export and FFmpeg for simple trims, fades, title cards, and restrained lower-thirds. Keep the terminal capture authentic and readable; a single short title and one or two labels are enough. FFmpeg is the lowest-overhead choice for this one-off edit. Remotion renders compositions from React and Motion Canvas is designed for animated vector scenes; both add a frame-rendering toolchain that is unnecessary unless a later brief requires custom motion graphics. Do not download stock footage or install either framework for this cut.

Use Cap's `social` export preset at 1920x1080/30 fps with filesize optimization, then inspect the MP4 with ffprobe and watch the complete export once for legibility, crop, audio, and accidental sensitive content. Keep the native `.cap` project and final MP4 under `~/Movies/pk-launch-demo`; do not upload or publish without a separate explicit request.

## Primary tool references

- [Cap CLI for agents](https://cap.so/agents), [installation and permissions](https://cap.so/docs/installation), [Cap source](https://github.com/CapSoftware/Cap)
- [FFmpeg filter documentation](https://ffmpeg.org/ffmpeg-filters.html)
- [Remotion](https://www.remotion.dev/)
- [Motion Canvas documentation](https://motioncanvas.io/docs/)
