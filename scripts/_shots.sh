#!/usr/bin/env bash
# 逐页截图 (仅用于人工/视觉核验, 不进仓库制品)
set -u
EDGE="/c/Program Files (x86)/Microsoft/Edge/Application/msedge.exe"
TOKEN="7f910bada30d5f312991d4e47e2c420b"
BASE="F:/Projects/Golang/GitHub/Private/VocabLock-v2/backend/.shot"
for tab in "$@"; do
  "$EDGE" --headless=new --disable-gpu --hide-scrollbars --no-first-run \
    --user-data-dir="$BASE/ud-$tab" \
    --window-size=1500,1900 --virtual-time-budget=14000 \
    --screenshot="$BASE/$tab.png" \
    "http://127.0.0.1:18420/dashboard?token=$TOKEN#$tab" 2>&1 | grep -v ERROR | tail -1
done
ls -la "$BASE"/world.png "$BASE"/dialog.png "$BASE"/sticker.png 2>&1
