#!/usr/bin/env python3
"""One-pass, local FFmpeg treatment for an authentic Cap-exported pk demo."""

from __future__ import annotations

import argparse
import html
import json
import os
import shutil
import subprocess
import sys
import tempfile
from pathlib import Path


FFMPEG_DEFAULT = "/opt/homebrew/bin/ffmpeg"
FFPROBE_DEFAULT = "/opt/homebrew/bin/ffprobe"
SIPS_DEFAULT = "/usr/bin/sips"
WIDTH, HEIGHT, FPS = 1920, 1080, 30


def run(args: list[str]) -> subprocess.CompletedProcess[str]:
    return subprocess.run(args, check=True, text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE)


def duration_of(ffprobe: str, source: Path) -> float:
    result = run([
        ffprobe, "-v", "error", "-show_entries", "format=duration",
        "-of", "default=noprint_wrappers=1:nokey=1", str(source),
    ])
    try:
        return float(result.stdout.strip())
    except ValueError as exc:
        raise ValueError("ffprobe did not return a valid source duration") from exc


def safe_text(value: str, label: str, maximum: int) -> str:
    value = " ".join(value.split())
    if len(value) > maximum:
        raise ValueError(f"{label} must be at most {maximum} characters")
    return value


def svg_text(lines: list[str], x: int, y: int, font_size: int, fill: str, weight: int = 600) -> str:
    tspans = []
    for index, line in enumerate(lines):
        dy = "0" if index == 0 else str(font_size * 1.3)
        tspans.append(f'<tspan x="{x}" dy="{dy}">{html.escape(line)}</tspan>')
    return (
        f'<text x="{x}" y="{y}" text-anchor="middle" fill="{fill}" '
        f'font-family="Arial, Helvetica, sans-serif" font-size="{font_size}" '
        f'font-weight="{weight}">{"".join(tspans)}</text>'
    )


def write_cards(directory: Path, title: str, subtitle: str, lower: str, outro: str) -> tuple[Path, Path, Path]:
    title_svg = directory / "title.svg"
    lower_svg = directory / "lower.svg"
    outro_svg = directory / "outro.svg"
    title_svg.write_text(
        '<svg xmlns="http://www.w3.org/2000/svg" width="1920" height="1080">'
        '<rect width="1920" height="1080" fill="#0b1524" fill-opacity="0.94"/>'
        '<rect x="0" y="0" width="1920" height="14" fill="#75f0c0"/>'
        '<rect x="792" y="356" width="336" height="6" rx="3" fill="#75f0c0"/>'
        + svg_text([title], 960, 505, 112, "#f4f8fb", 700)
        + svg_text([subtitle], 960, 602, 42, "#b9cad6", 400)
        + '<text x="960" y="1000" text-anchor="middle" fill="#75f0c0" font-family="Arial, Helvetica, sans-serif" font-size="24" letter-spacing="4">PK · REAL PRODUCT DEMO</text>'
        + '</svg>',
        encoding="utf-8",
    )
    lower_svg.write_text(
        '<svg xmlns="http://www.w3.org/2000/svg" width="1920" height="1080">'
        '<rect x="72" y="900" width="760" height="112" rx="10" fill="#0b1524" fill-opacity="0.91"/>'
        '<rect x="72" y="900" width="8" height="112" rx="4" fill="#75f0c0"/>'
        f'<text x="112" y="970" fill="#f4f8fb" font-family="Arial, Helvetica, sans-serif" font-size="34" font-weight="600">{html.escape(lower)}</text>'
        '</svg>',
        encoding="utf-8",
    )
    outro_svg.write_text(
        '<svg xmlns="http://www.w3.org/2000/svg" width="1920" height="1080">'
        '<rect width="1920" height="1080" fill="#0b1524" fill-opacity="0.98"/>'
        '<rect x="0" y="0" width="1920" height="14" fill="#75f0c0"/>'
        + svg_text(["pk"], 960, 480, 100, "#f4f8fb", 700)
        + svg_text([outro], 960, 566, 40, "#b9cad6", 400)
        + '<rect x="892" y="660" width="136" height="6" rx="3" fill="#75f0c0"/>'
        + '</svg>',
        encoding="utf-8",
    )
    return title_svg, lower_svg, outro_svg


def rasterize(sips: str, svg: Path, png: Path) -> None:
    # Render only three transparent title overlays; no full-resolution video intermediates.
    run([sips, "-s", "format", "png", str(svg), "--out", str(png)])


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("input", type=Path, help="real Cap-exported MP4")
    parser.add_argument("output", type=Path, help="new MP4 output path")
    parser.add_argument("--start", type=float, default=0.0, help="trim start in seconds (default: 0)")
    parser.add_argument("--duration", type=float, default=45.0, help="final length, 30–60 seconds (default: 45)")
    parser.add_argument("--title", default="pk", help="opening title (default: pk)")
    parser.add_argument("--subtitle", default="A terminal-native coding agent", help="opening subtitle")
    parser.add_argument("--lower-third", default="Live pk terminal session", help="brief lower-third label")
    parser.add_argument("--end-line", default="Coding work, in your workspace", help="ending line")
    parser.add_argument("--ffmpeg", default=os.environ.get("FFMPEG", FFMPEG_DEFAULT))
    parser.add_argument("--ffprobe", default=os.environ.get("FFPROBE", FFPROBE_DEFAULT))
    parser.add_argument("--sips", default=SIPS_DEFAULT, help="macOS SVG-to-PNG renderer")
    parser.add_argument("--force", action="store_true", help="replace an existing output file")
    args = parser.parse_args()

    source = args.input.expanduser().resolve()
    destination = args.output.expanduser().resolve()
    if not source.is_file():
        parser.error(f"input does not exist or is not a file: {source}")
    if source == destination:
        parser.error("output must not overwrite the source clip")
    if destination.exists() and not args.force:
        parser.error(f"output already exists (pass --force to replace): {destination}")
    if args.start < 0 or not 30 <= args.duration <= 60:
        parser.error("start must be non-negative and duration must be between 30 and 60 seconds")
    try:
        title = safe_text(args.title, "title", 36)
        subtitle = safe_text(args.subtitle, "subtitle", 80)
        lower = safe_text(args.lower_third, "lower-third", 52)
        end_line = safe_text(args.end_line, "end-line", 80)
        source_duration = duration_of(args.ffprobe, source)
    except (ValueError, subprocess.CalledProcessError) as exc:
        print(f"pk demo edit: {exc}", file=sys.stderr)
        return 2
    if args.start + args.duration > source_duration + 0.001:
        parser.error(f"requested segment ends at {args.start + args.duration:.3f}s, but source is {source_duration:.3f}s")

    if shutil.which(args.ffmpeg) is None and not Path(args.ffmpeg).is_file():
        parser.error(f"ffmpeg not found: {args.ffmpeg}")
    if shutil.which(args.ffprobe) is None and not Path(args.ffprobe).is_file():
        parser.error(f"ffprobe not found: {args.ffprobe}")
    if shutil.which(args.sips) is None and not Path(args.sips).is_file():
        parser.error(f"sips not found: {args.sips}")

    destination.parent.mkdir(parents=True, exist_ok=True)
    descriptor, temp_path = tempfile.mkstemp(prefix=f".{destination.stem}.", suffix=".partial.mp4", dir=destination.parent)
    os.close(descriptor)
    temp_output = Path(temp_path)
    try:
        with tempfile.TemporaryDirectory(prefix="pk-demo-edit-") as temp_name:
            temp = Path(temp_name)
            svgs = write_cards(temp, title, subtitle, lower, end_line)
            pngs = (temp / "title.png", temp / "lower.png", temp / "outro.png")
            for svg, png in zip(svgs, pngs):
                rasterize(args.sips, svg, png)
            lower_end = min(10.0, args.duration - 3.1)
            outro_start = args.duration - 3.0
            filters = (
                "[0:v]scale=1920:1080:force_original_aspect_ratio=decrease:flags=lanczos,"
                "pad=1920:1080:(ow-iw)/2:(oh-ih)/2:color=0x0b1524,setsar=1,fps=30,format=yuv420p[v0];"
                f"[v0][1:v]overlay=0:0:shortest=1:eof_action=repeat:enable='lt(t,3)'[v1];"
                f"[v1][2:v]overlay=0:0:shortest=1:eof_action=repeat:enable='between(t,4,{lower_end:.3f})'[v2];"
                f"[v2][3:v]overlay=0:0:shortest=1:eof_action=repeat:enable='gte(t,{outro_start:.3f})',format=yuv420p[vout]"
            )
            command = [
                args.ffmpeg, "-hide_banner", "-loglevel", "error", "-y",
                "-ss", f"{args.start:.3f}", "-i", str(source),
                "-loop", "1", "-framerate", "30", "-i", str(pngs[0]),
                "-loop", "1", "-framerate", "30", "-i", str(pngs[1]),
                "-loop", "1", "-framerate", "30", "-i", str(pngs[2]),
                "-filter_complex", filters, "-map", "[vout]", "-t", f"{args.duration:.3f}",
                "-an", "-c:v", "libx264", "-preset", "veryfast", "-crf", "22",
                "-pix_fmt", "yuv420p", "-movflags", "+faststart", str(temp_output),
            ]
            run(command)
        if not temp_output.is_file() or temp_output.stat().st_size == 0:
            raise RuntimeError("ffmpeg did not create a non-empty output")
        if args.force:
            os.replace(temp_output, destination)
        else:
            # Linking makes the no-overwrite guarantee atomic if another file
            # appears after the initial existence check.
            os.link(temp_output, destination)
            temp_output.unlink()
        probe = run([
            args.ffprobe, "-v", "error", "-show_entries", "format=duration,size:stream=codec_name,width,height,r_frame_rate",
            "-of", "json", str(destination),
        ])
        metadata = json.loads(probe.stdout)
        print(json.dumps({"output": str(destination), "format": metadata.get("format"), "streams": metadata.get("streams")}, indent=2))
        return 0
    except (OSError, RuntimeError, subprocess.CalledProcessError, json.JSONDecodeError) as exc:
        try:
            temp_output.unlink(missing_ok=True)
        except OSError:
            pass
        print(f"pk demo edit: {exc}", file=sys.stderr)
        if isinstance(exc, subprocess.CalledProcessError) and exc.stderr:
            print(exc.stderr.strip(), file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
