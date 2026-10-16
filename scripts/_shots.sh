#!/usr/bin/env bash
# 逐页截图 (仅用于人工/视觉核验, 不进仓库制品)
#
# 用法: [PORT=8420] [TOKEN=xxx] ./scripts/_shots.sh world dialog sticker
# TOKEN 缺省从 data/convo-token.txt 读 (convo 首次启动生成的访问令牌)。
# 截图落在本仓库的 .shot/ —— 此前写死成旧仓库 VocabLock-v2/backend/.shot。
set -u
EDGE="/c/Program Files (x86)/Microsoft/Edge/Application/msedge.exe"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
BASE="$ROOT/.shot"
PORT="${PORT:-18420}"
TOKEN="${TOKEN:-}"
if [ -z "$TOKEN" ] && [ -f "$ROOT/data/convo-token.txt" ]; then
  TOKEN="$(cat "$ROOT/data/convo-token.txt")"
fi
if [ -z "$TOKEN" ]; then
  echo "!! 缺 TOKEN —— 传 TOKEN=... 或先让 data/convo-token.txt 存在" >&2
  exit 1
fi
mkdir -p "$BASE"
for tab in "$@"; do
  "$EDGE" --headless=new --disable-gpu --hide-scrollbars --no-first-run --user-data-dir="$BASE/ud-$tab" --window-size=1500,1900 --virtual-time-budget=14000 --screenshot="$BASE/$tab.png" "http://127.0.0.1:$PORT/dashboard?token=$TOKEN#$tab" 2>&1 | grep -v ERROR | tail -1
done
ls -la "$BASE"/*.png 2>&1
