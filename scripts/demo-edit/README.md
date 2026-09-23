# Local pk demo edit

`edit.py` adds restrained mint/navy title cards and one lower-third to a real
Cap-exported MP4. It pads rather than crops the source, outputs 1920×1080 at
30 fps, removes audio, and encodes the picture in one FFmpeg pass. Only three
small transparent title-card PNGs are rendered as temporary files; they are
deleted after the export. No stock footage or network access is used.

After Cap has exported and the complete source has been reviewed, run:

```sh
python3 scripts/demo-edit/edit.py \
  "$HOME/Movies/pk-launch-demo/pk-launch-demo.mp4" \
  "$HOME/Movies/pk-launch-demo/pk-launch-final.mp4" \
  --start 0 --duration 45 \
  --lower-third "Live pk terminal session"
```

Duration must be 30–60 seconds and the selected interval must exist in the
source. Pass `--force` only when intentionally replacing the chosen output.
The script prints ffprobe metadata after a successful encode. Watch the entire
export once to check text, framing, and sensitive information before sharing.

This step does not capture the desktop or publish anything. Use only a
reviewed, real Cap export as input. Adjust the lower-third wording to match
what the recording actually shows.
