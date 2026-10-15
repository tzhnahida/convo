#!/usr/bin/env bash
# convo → Pi 部署。
#
# ⚠️ **这个脚本存在的理由是一次真事故 (2026-10-14)**。
#
# 当时我用 pwsh 的 scp 推二进制, 命令里夹了个 `$(date +%H%M%S)`, 被 PowerShell
# 当成 Get-Date 求值并报错。scp 静默没跑成, 而 `convo.new` 还躺着上一次的旧文件 ——
# 于是 `mv convo.new convo` 把**同一个旧二进制**搬了个位置, 还打印了 `active`。
#
# 症状有多隐蔽: 服务活着、日志正常、命令全绿, 只有 `md5sum` 和编译产物对不上。
# 我是在"新加的功能一条日志都没出"时才回头查的。
#
# 所以这里每一步都要**可验证**:
#   ① 两端哈希必须相等才继续 (对不上就中止, 绝不安装);
#   ② 装完再核一次运行中的哈希;
#   ③ 备份名带时间戳, 且**不依赖 shell 变量在远端展开**。
set -euo pipefail

PI="${PI:-pi}"
DIR="${DIR:-/home/tzh/convo}"
BIN=/tmp/convo-arm64

cd "$(dirname "$0")/.."

echo "=== 页面闸 · JS 语法 (node --check) ==="
# ⚠️ 整页 HTML 是一个 Go raw string —— JS 里的语法错误**编译期报不出来**。
# 它的表现是「控制台下半部分全白」, 而那和「还没有数据」长得一模一样:
# 这一版就因为少一层括号白屏过一次, 是靠人眼看截图才发现的。
# 所以这里再加一道硬闸: 抽不出 <script> 块, 或者 node 不在, 一律**中止部署**
# (fail closed) —— 一个只在自己方便时才生效的闸门, 比不写更糟。
command -v node >/dev/null 2>&1 || { echo "!! 找不到 node —— 页面语法无法核验, 中止" >&2; exit 1; }
node -e '
const fs=require("fs");
const s=fs.readFileSync("internal/convo/dashboard_html.go","utf8");
const i=s.indexOf("<script>"), j=s.indexOf("</script>", i);
if(i<0||j<=i){console.error("!! 抽不出 <script> 块");process.exit(1);}
fs.writeFileSync(".page-check.js", s.slice(i+8,j));
' || { echo "!! 抽取页面脚本失败 —— 中止" >&2; exit 1; }
node --check .page-check.js || { rm -f .page-check.js; echo "!! 页面 JS 语法错误 —— 装上去就是白屏, 中止" >&2; exit 1; }
rm -f .page-check.js
echo "    ✓ 页面 JS 语法通过"
echo "=== 1/5 交叉编译 (linux/arm64) ==="
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o "$BIN" ./cmd/convo
LOCAL_MD5=$(md5sum "$BIN" | cut -d" " -f1)
echo "    local md5 = $LOCAL_MD5"

echo "=== 2/5 上传 ==="
scp -o ConnectTimeout=15 "$BIN" "$PI:$DIR/convo.new"
REMOTE_MD5=$(ssh "$PI" "md5sum $DIR/convo.new | cut -d\" \" -f1")
echo "    remote md5 = $REMOTE_MD5"
if [ "$LOCAL_MD5" != "$REMOTE_MD5" ]; then
  echo "!! 传输后哈希不一致 —— 中止, 不安装" >&2
  exit 1
fi
echo "    ✓ 两端一致"

echo "=== 3/5 备份 + 安装 ==="
# 时间戳在**本地**取好再传进去 —— 远端展开会被外层 shell 先吃掉一次。
TAG=$(date +%Y%m%d-%H%M%S)
ssh "$PI" "cd $DIR && cp convo convo.bak-$TAG && mv convo.new convo && chmod +x convo && echo 备份=convo.bak-$TAG"

echo "=== 4/5 重启 ==="
ssh "$PI" "sudo -n systemctl restart convo && sleep 6 && systemctl is-active convo"

echo "=== 5/5 核对运行中的二进制 ==="
RUN_MD5=$(ssh "$PI" "md5sum $DIR/convo | cut -d\" \" -f1")
echo "    running md5 = $RUN_MD5"
if [ "$LOCAL_MD5" != "$RUN_MD5" ]; then
  echo "!! 跑起来的不是刚编的那个 —— 这就是 2026-10-14 那次事故的形状" >&2
  exit 1
fi
echo "    ✓ 部署完成, 跑的就是刚编的"
