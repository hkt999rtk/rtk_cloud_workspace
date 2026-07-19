#!/usr/bin/env bash
set -euo pipefail

out_dir="${1:-$(cd "$(dirname "$0")" && pwd)}"
mkdir -p "$out_dir"

ffmpeg_bin="${FFMPEG_BIN:-ffmpeg}"
ffprobe_bin="${FFPROBE_BIN:-ffprobe}"
clip="$out_dir/clip_1080p_h264_3mbps_15s.mp4"
thumbnail="$out_dir/thumbnail_1080p.jpg"

"$ffmpeg_bin" -hide_banner -loglevel error -y \
  -f lavfi -i "testsrc2=size=1920x1080:rate=30" -t 15 -an \
  -c:v libx264 -b:v 3M -minrate 3M -maxrate 3M -bufsize 6M \
  -pix_fmt yuv420p -movflags +faststart "$clip"
"$ffmpeg_bin" -hide_banner -loglevel error -y \
  -f lavfi -i "testsrc2=size=1920x1080:rate=1" -frames:v 1 \
  -q:v 3 "$thumbnail"

codec=$($ffprobe_bin -v error -select_streams v:0 -show_entries stream=codec_name,width,height \
  -of csv=p=0 "$clip")
duration=$($ffprobe_bin -v error -show_entries format=duration -of default=nw=1:nk=1 "$clip")
if [[ "$codec" != h264,*1920,*1080* ]]; then
  echo "unexpected clip stream: $codec" >&2
  exit 1
fi
awk -v duration="$duration" 'BEGIN { if (duration < 14.9 || duration > 15.1) exit 1 }' || {
  echo "unexpected clip duration: $duration" >&2
  exit 1
}

echo "clip=$clip"
echo "thumbnail=$thumbnail"
wc -c "$clip" "$thumbnail"
