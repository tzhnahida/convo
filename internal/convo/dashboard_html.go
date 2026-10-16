package convo

// ─────────────────────────────────────────────────────────────
// 控制台页面本体 (纯静态: CSS + 原生 JS, 零构建 / 零 CDN / 零 npm)。
//
// ⚠️ 为什么不用 Tailwind CDN 或 admin/ 那套 Svelte:
// 这一页是**观测手段**。CDN 一断页面就白屏, 而白屏和「服务挂了」长得一模一样;
// npm+vite 那条链路「改了但没构建」和「没改」也长得一模一样。
// 这个项目已经在"部署了旧二进制"上栽过一次 —— 观测手段本身不该有依赖风险。
// 所以 Modern Gradient 的风格是**手写 CSS 实现的**: 视觉一致, 但零外部请求。
//
// ⚠️ Go raw string 里不能出现反引号 → 本文件内 JS 一律引号拼接, 不用模板字符串。
// ⚠️ 页面不含 token: 从 URL 的 ?token= 读一次存 localStorage, 之后所有 fetch/img 都带上。
// ⚠️ SPEC 数组是「页面要什么」的唯一清单。dashboard_page_test.go 拿它和 server.go
//    实际注册的路由**逐条对账** —— 少注册一条, 那张卡就永远是红的, 而在此之前
//    这种错是静默的 (只表现为"这块没数据")。
// ─────────────────────────────────────────────────────────────

const dashboardHTML = `<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>convo 控制台</title>
<style>
*,*::before,*::after{box-sizing:border-box}
:root{
  --ink2:rgba(255,255,255,.78);--ink3:rgba(255,255,255,.55);--ink4:rgba(255,255,255,.35);
  --glass:rgba(255,255,255,.07);--glass2:rgba(255,255,255,.13);
  --line:rgba(255,255,255,.16);--line2:rgba(255,255,255,.32);
  --r:18px;--r2:26px;
}
html,body{margin:0;min-height:100%}
body{
  background:
    radial-gradient(1100px 720px at 6% -10%, rgba(139,92,246,.38), transparent 62%),
    radial-gradient(980px 640px at 94% 0%, rgba(217,70,239,.28), transparent 58%),
    radial-gradient(900px 900px at 55% 112%, rgba(34,211,238,.20), transparent 60%),
    linear-gradient(165deg,#020617 0%,#1e1b4b 46%,#0b1020 100%);
  background-attachment:fixed;color:var(--ink2);
  font:14px/1.62 "Segoe UI",system-ui,-apple-system,"PingFang SC","Microsoft YaHei",sans-serif;
  -webkit-font-smoothing:antialiased;padding-bottom:60px;
}
.gtext{background:linear-gradient(90deg,#a78bfa,#e879f9,#22d3ee);-webkit-background-clip:text;background-clip:text;color:transparent}
.mono{font-family:ui-monospace,"Cascadia Code",Consolas,monospace}
.top{position:sticky;top:0;z-index:30;padding:14px 20px 0;
  background:linear-gradient(180deg,rgba(2,6,23,.88),rgba(2,6,23,.45) 72%,transparent);
  backdrop-filter:blur(14px);-webkit-backdrop-filter:blur(14px)}
.toprow{display:flex;align-items:center;gap:16px;flex-wrap:wrap}
.brand{display:flex;align-items:center;gap:12px;min-width:0}
.logo{width:40px;height:40px;border-radius:14px;flex:none;display:grid;place-items:center;font-size:19px;color:#fff;
  background-image:linear-gradient(135deg,#8b5cf6,#d946ef,#22d3ee);background-size:200% auto;
  box-shadow:0 10px 28px -10px rgba(217,70,239,.85)}
.brand h1{margin:0;font-size:19px;font-weight:700;line-height:1.2}
.brand p{margin:1px 0 0;font-size:11.5px;color:var(--ink4)}
.spacer{flex:1 1 auto}
.tools{display:flex;align-items:center;gap:9px;flex-wrap:wrap}
.clock{font-size:12px;color:var(--ink3);font-variant-numeric:tabular-nums}
.sw{display:inline-flex;align-items:center;gap:7px;font-size:12px;color:var(--ink3);cursor:pointer;
  padding:6px 12px;border-radius:14px;border:1px solid var(--line);background:var(--glass);transition:all .3s}
.sw:hover{border-color:var(--line2);color:#fff}
.sw input{accent-color:#a855f7;width:14px;height:14px;cursor:pointer;margin:0}
.btn{display:inline-flex;align-items:center;gap:7px;padding:9px 17px;border-radius:16px;
  font:600 13px/1 inherit;color:#fff;cursor:pointer;border:1px solid var(--line);background:var(--glass2);
  transition:transform .5s cubic-bezier(.22,.61,.36,1),box-shadow .4s,background .3s,border-color .3s,background-position .5s}
.btn:hover{background:rgba(255,255,255,.22);border-color:var(--line2);transform:translateY(-2px) scale(1.02);
  box-shadow:0 14px 34px -14px rgba(255,255,255,.35)}
.btn:active{transform:scale(.95)}
.btn:focus-visible,.tab:focus-visible,input:focus-visible,select:focus-visible{outline:2px solid #c4b5fd;outline-offset:2px}
.btn[disabled]{opacity:.5;cursor:progress}
.btn.pri{background-image:linear-gradient(90deg,#8b5cf6,#d946ef,#8b5cf6);background-size:200% auto;background-position:0% center;
  border-color:transparent;box-shadow:0 10px 28px -12px rgba(217,70,239,.95)}
.btn.pri:hover{background-position:100% center;box-shadow:0 18px 46px -14px rgba(217,70,239,1)}
.btn.danger{background-image:linear-gradient(90deg,#e11d48,#f97316,#e11d48);background-size:200% auto;background-position:0% center;border-color:transparent}
.btn.danger:hover{background-position:100% center;box-shadow:0 16px 40px -14px rgba(249,115,22,.9)}
.btn.sm{padding:5px 11px;border-radius:12px;font-size:11.5px}
.tabs{display:flex;gap:8px;overflow-x:auto;padding:14px 0 4px;scrollbar-width:thin}
.tabs::-webkit-scrollbar{height:5px}
.tabs::-webkit-scrollbar-thumb{background:rgba(255,255,255,.2);border-radius:3px}
.tab{padding:8px 15px;border-radius:14px;border:1px solid transparent;background:rgba(255,255,255,.055);
  color:var(--ink3);cursor:pointer;white-space:nowrap;font:500 13px/1 inherit;transition:all .35s cubic-bezier(.22,.61,.36,1)}
.tab:hover{color:#fff;background:rgba(255,255,255,.14);transform:translateY(-1px)}
.tab[aria-selected=true]{background-image:linear-gradient(90deg,rgba(139,92,246,.95),rgba(217,70,239,.95));color:#fff;
  border-color:rgba(255,255,255,.3);box-shadow:0 10px 26px -10px rgba(217,70,239,.9)}
main{padding:14px 20px 0}
.grid{display:grid;gap:16px;grid-template-columns:repeat(auto-fit,minmax(400px,1fr));align-items:start}
.span2{grid-column:1/-1}
.card{background:var(--glass);border:1px solid var(--line);border-radius:var(--r2);padding:18px 20px 20px;
  backdrop-filter:blur(18px) saturate(140%);-webkit-backdrop-filter:blur(18px) saturate(140%);
  box-shadow:0 12px 34px -18px rgba(139,92,246,.75);
  transition:transform .5s cubic-bezier(.22,.61,.36,1),box-shadow .5s,border-color .5s;min-width:0}
.card:hover{transform:translateY(-4px) scale(1.006);border-color:var(--line2);
  box-shadow:0 30px 70px -30px rgba(217,70,239,.85)}
.card.flat:hover{transform:none;box-shadow:0 12px 34px -18px rgba(139,92,246,.75)}
.card>header{display:flex;align-items:baseline;gap:10px;flex-wrap:wrap;margin-bottom:12px}
.card>header h2{margin:0;font-size:14.5px;font-weight:700}
.card>header .meta{font-size:11px;color:var(--ink4);margin-left:auto;text-align:right}
.empty{color:var(--ink4);font-size:12.5px;margin:6px 0}
.hint{font-size:11.5px;color:var(--ink4);margin-top:10px;line-height:1.65}
.err{color:#fda4af;font-size:12.5px;word-break:break-word;margin:6px 0}
.kpis{display:grid;gap:10px;grid-template-columns:repeat(auto-fit,minmax(120px,1fr))}
.kpi{padding:11px 14px;border-radius:var(--r);background:rgba(255,255,255,.055);border:1px solid rgba(255,255,255,.1);
  transition:transform .5s cubic-bezier(.22,.61,.36,1),background .3s,box-shadow .5s;min-width:0}
.kpi:hover{transform:translateY(-3px);background:rgba(255,255,255,.11);box-shadow:0 16px 36px -18px rgba(34,211,238,.9)}
.kpi b{display:block;font-size:23px;font-weight:700;line-height:1.18;word-break:break-word}
.kpi span{display:block;font-size:11px;color:var(--ink4);margin-top:1px}
.tw{overflow:auto;max-height:64vh;border-radius:var(--r);border:1px solid rgba(255,255,255,.11);background:rgba(2,6,23,.25)}
.tw::-webkit-scrollbar{width:8px;height:8px}
.tw::-webkit-scrollbar-thumb{background:rgba(255,255,255,.18);border-radius:4px}
table{width:100%;border-collapse:collapse;font-size:12.5px}
thead th{position:sticky;top:0;z-index:1;text-align:left;padding:9px 11px;color:var(--ink3);font-weight:600;font-size:11.5px;
  white-space:nowrap;background:rgba(12,14,34,.94);border-bottom:1px solid var(--line)}
tbody td{text-align:left;padding:8px 11px;border-bottom:1px solid rgba(255,255,255,.075);
  color:var(--ink2);vertical-align:top;word-break:break-word}
tbody tr{transition:background .25s}
tbody tr:hover{background:rgba(255,255,255,.06)}
tbody tr.click{cursor:pointer}
td.det{background:rgba(2,6,23,.55);padding:14px}
.bar{height:6px;border-radius:4px;background:rgba(255,255,255,.13);overflow:hidden;min-width:52px}
.bar i{display:block;height:100%;border-radius:4px;background-image:linear-gradient(90deg,#8b5cf6,#d946ef)}
.bar.ok i{background-image:linear-gradient(90deg,#22d3ee,#34d399)}
.bar.warn i{background-image:linear-gradient(90deg,#fbbf24,#f97316)}
.barrow{display:flex;align-items:center;gap:8px;min-width:120px}
.barrow span{font-size:11px;color:var(--ink4);font-variant-numeric:tabular-nums;white-space:nowrap}
.pill{display:inline-block;padding:1.5px 9px;border-radius:11px;font-size:11px;line-height:17px;
  border:1px solid var(--line);background:rgba(255,255,255,.08);color:var(--ink2);white-space:nowrap}
.pill.ok{border-color:rgba(52,211,153,.5);background:rgba(52,211,153,.16);color:#a7f3d0}
.pill.warn{border-color:rgba(251,191,36,.5);background:rgba(251,191,36,.16);color:#fde68a}
.pill.bad{border-color:rgba(251,113,133,.5);background:rgba(251,113,133,.16);color:#fecdd3}
.pill.v{border-color:rgba(167,139,250,.5);background:rgba(139,92,246,.2);color:#ddd6fe}
.chip{display:inline-block;margin:2px 3px 2px 0;padding:2px 9px;border-radius:10px;
  background:rgba(255,255,255,.07);border:1px solid rgba(255,255,255,.11);font-size:11.5px;color:var(--ink2)}
.chip.unsure{border-color:rgba(251,191,36,.45);background:rgba(251,191,36,.13);color:#fde68a}
.pre{white-space:pre-wrap;word-break:break-word;font-family:ui-monospace,"Cascadia Code",Consolas,monospace;
  font-size:11.5px;line-height:1.8;max-height:420px;overflow:auto;padding:13px;border-radius:var(--r);
  background:rgba(2,6,23,.55);border:1px solid rgba(255,255,255,.1);color:var(--ink2)}
.sg{padding:1.5px 2px;border-radius:4px}
.sg0{background:rgba(139,92,246,.32)}.sg1{background:rgba(217,70,239,.32)}
.sg2{background:rgba(34,211,238,.28)}.sg3{background:rgba(52,211,153,.28)}
.sg4{background:rgba(251,191,36,.28)}.sg5{background:rgba(249,115,22,.28)}
.sg6{background:rgba(236,72,153,.28)}.sg7{background:rgba(59,130,246,.30)}
input[type=text],input[type=search],select{padding:9px 14px;border-radius:14px;color:#fff;font:13px/1 inherit;
  background:rgba(255,255,255,.08);border:1px solid var(--line);transition:all .3s;min-width:0}
input::placeholder{color:rgba(255,255,255,.38)}
input[type=text]:hover,input[type=search]:hover,select:hover{border-color:var(--line2)}
input[type=text]:focus,input[type=search]:focus,select:focus{border-color:rgba(139,92,246,.75);
  box-shadow:0 0 0 3px rgba(139,92,246,.22);outline:none}
select option{background:#1e1b4b;color:#fff}
.bar2{display:flex;gap:9px;align-items:center;flex-wrap:wrap;margin-bottom:12px}
.quote{padding:13px 17px;border-radius:var(--r);border:1px solid rgba(167,139,250,.35);
  background:linear-gradient(90deg,rgba(139,92,246,.20),rgba(217,70,239,.12));font-size:14.5px;color:#fff;margin-bottom:14px}
.castgrid{display:grid;gap:11px;grid-template-columns:repeat(auto-fit,minmax(290px,1fr))}
.cast{padding:13px 15px;border-radius:var(--r);border:1px solid rgba(255,255,255,.12);
  background:rgba(255,255,255,.05);transition:transform .5s cubic-bezier(.22,.61,.36,1),border-color .4s,box-shadow .5s;min-width:0}
.cast:hover{transform:translateY(-3px);border-color:var(--line2);box-shadow:0 18px 44px -22px rgba(139,92,246,.95)}
.cast .nm{font-size:14.5px;font-weight:700;color:#fff;margin-right:8px}
.cast p{margin:7px 0 0;font-size:12.5px;color:var(--ink3);word-break:break-word}
.cast p b{color:var(--ink2);font-weight:600}
.cast .st{color:#ddd6fe}
.day{display:flex;gap:10px;align-items:flex-start;padding:7px 0;border-bottom:1px solid rgba(255,255,255,.07)}
.day b{flex:none;width:46px;font-size:12px;color:var(--ink3);padding-top:3px}
.day .slots{display:flex;flex-wrap:wrap;gap:4px;flex:1;min-width:0}
.slot{padding:2px 8px;border-radius:9px;font-size:11px;border-left:3px solid #64748b;background:rgba(255,255,255,.07);white-space:nowrap}
.day.today b{color:#e879f9;font-weight:700}
.sgrid{display:grid;gap:11px;grid-template-columns:repeat(auto-fill,minmax(124px,1fr))}
.st{padding:8px;border-radius:var(--r);border:1px solid rgba(255,255,255,.12);background:rgba(255,255,255,.05);text-align:center;
  transition:transform .5s cubic-bezier(.22,.61,.36,1),box-shadow .5s,border-color .4s}
.st:hover{transform:translateY(-4px) scale(1.03);border-color:var(--line2);box-shadow:0 22px 48px -22px rgba(217,70,239,.95)}
.st img{width:100%;height:96px;object-fit:contain;border-radius:12px;background:rgba(2,6,23,.4)}
.st small{display:block;font-size:10.5px;color:var(--ink4);margin-top:5px;word-break:break-all}
.st.retired{opacity:.42;filter:grayscale(1)}
.toast{position:fixed;left:50%;bottom:26px;transform:translate(-50%,26px);opacity:0;pointer-events:none;
  padding:11px 22px;border-radius:16px;color:#fff;font-size:13px;font-weight:600;z-index:60;max-width:80vw;text-align:center;
  background-image:linear-gradient(90deg,#8b5cf6,#d946ef);background-size:200% auto;
  box-shadow:0 20px 50px -18px rgba(217,70,239,1);transition:all .4s cubic-bezier(.22,.61,.36,1)}
.toast.on{opacity:1;transform:translate(-50%,0)}
.toast.bad{background-image:linear-gradient(90deg,#e11d48,#f97316);box-shadow:0 20px 50px -18px rgba(249,115,22,1)}
@media (prefers-reduced-motion:reduce){*,*::before,*::after{animation-duration:.001ms!important;transition-duration:.001ms!important}}
/* 软刷新 (自动刷新 / 点刷新): **一处动画都不放**。
   切页放动画是对的 —— 那是"新内容"的提示; 而刷新放动画等于整页重放一遍,
   看起来就是 owner 说的"闪一下", 而且每 30 秒闪一次。
   ⚠️ 只关动画, **不关终态**: 图表照样是画完的样子 (与 reduced-motion 同一条纪律)。 */
.noanim *,.noanim *::before,.noanim *::after{animation:none!important}
@media (max-width:700px){.grid{grid-template-columns:1fr}main,.top{padding-left:13px;padding-right:13px}.brand p{display:none}}
/* ——— 图表 · 卡片动效 · 骨架屏 (2026-10-16) ———
   ⚠️ 全部手写: 零外部请求是硬约束 —— 观测手段不该有断网风险。
   ⚠️ 每一个动效都必须在 prefers-reduced-motion 下关掉, 见本文件末尾那一段。 */
.ch{width:100%;height:auto;display:block;overflow:visible}
.ch .gl{stroke:rgba(255,255,255,.09);stroke-width:.35}
.ch .ln{fill:none;stroke-width:1.7;stroke-linecap:round;stroke-linejoin:round;
  stroke-dasharray:2400;stroke-dashoffset:2400;animation:dash 1.5s cubic-bezier(.22,.9,.28,1) forwards}
.ch .ar{stroke:none;opacity:.14;animation:fade .9s .25s ease-out backwards}
.ch .dt{fill:#e2e8f0;stroke:rgba(2,6,23,.9);stroke-width:.7}
@keyframes dash{to{stroke-dashoffset:0}}
@keyframes fade{from{opacity:0}to{opacity:.14}}
.cbars{display:flex;align-items:flex-end;gap:2px;height:86px;padding:2px 0}
.cbars i{flex:1 1 0;min-width:3px;border-radius:3px 3px 1px 1px;transform-origin:bottom;
  background-image:linear-gradient(180deg,#a78bfa,#22d3ee);animation:grow .55s cubic-bezier(.22,.9,.28,1) backwards}
@keyframes grow{from{transform:scaleY(.02);opacity:.4}}
.caxis{display:flex;justify-content:space-between;font-size:11px;color:var(--ink4);margin-top:4px}
.ring{display:flex;align-items:center;gap:16px}
.ring svg{width:108px;height:108px;flex:none;transform:rotate(-90deg)}
.ring .rv{stroke-dasharray:264;stroke-dashoffset:264;animation:ring 1.1s cubic-bezier(.22,.9,.28,1) forwards}
@keyframes ring{to{stroke-dashoffset:var(--to,264)}}
.ringtx{min-width:0}
.ringtx b{display:block;font-size:26px;line-height:1.1}
.ringtx span{font-size:11.5px;color:var(--ink3)}
.heat{display:grid;gap:3px;margin-top:6px}
.heat i{aspect-ratio:1/1;border-radius:3px;background:rgba(255,255,255,.05);
  animation:pop .4s ease-out backwards}
@keyframes pop{from{transform:scale(.3);opacity:0}}
.legend{display:flex;gap:14px;flex-wrap:wrap;font-size:12px;color:var(--ink3);margin:6px 0 0}
.legend span{display:inline-flex;align-items:center;gap:6px}
.legend i{width:10px;height:10px;border-radius:3px;display:inline-block}
.tl{list-style:none;margin:4px 0 0;padding:0 0 0 16px;border-left:1px dashed var(--line)}
.tl li{position:relative;margin:0 0 9px}
.tl li:before{content:"";position:absolute;left:-21px;top:6px;width:8px;height:8px;border-radius:50%;
  background-image:linear-gradient(135deg,#a78bfa,#22d3ee);box-shadow:0 0 0 3px rgba(167,139,250,.16)}
.tl .t{font-size:11.5px;color:var(--ink4);font-variant-numeric:tabular-nums}
.ring2{display:flex;gap:20px;flex-wrap:wrap;margin-bottom:8px}
.card h3.sub{margin:12px 0 4px;font-size:12.5px;font-weight:600;color:var(--ink2)}
.stat{display:flex;align-items:baseline;gap:8px;margin:0 0 6px}
.stat b{font-size:22px;line-height:1.15}
.stat span{font-size:12px;color:var(--ink3)}
.card{animation:up .5s cubic-bezier(.22,.9,.28,1) backwards}
.card:nth-child(1){animation-delay:.02s}.card:nth-child(2){animation-delay:.07s}
.card:nth-child(3){animation-delay:.12s}.card:nth-child(4){animation-delay:.17s}
.card:nth-child(5){animation-delay:.22s}.card:nth-child(6){animation-delay:.27s}
.card:nth-child(n+7){animation-delay:.32s}
@keyframes up{from{opacity:0;transform:translateY(10px) scale(.995)}}
.sk{height:12px;border-radius:6px;margin:6px 0;
  background-image:linear-gradient(90deg,rgba(255,255,255,.05),rgba(255,255,255,.16),rgba(255,255,255,.05));
  background-size:200% 100%;animation:shim 1.25s linear infinite}
@keyframes shim{to{background-position:-200% 0}}
/* ⚠️ 降级不是可选项: 关掉动画之后页面必须**照样能用** —— 所以上面每一条动效的终态
   都是"内容可见"的那一侧 (dashoffset 归零 / 高度就是最终高度 / 透明度 1)。 */
@media (prefers-reduced-motion: reduce){
  *{animation:none !important;transition:none !important}
  .ch .ln{stroke-dashoffset:0}
  .ch .ar{opacity:.14}
  .ring .rv{stroke-dashoffset:var(--to,264)}
}
</style></head><body>
<header class="top">
  <div class="toprow">
    <div class="brand">
      <div class="logo" aria-hidden="true">◆</div>
      <div><h1 class="gtext">convo 控制台</h1><p id="sub">正在连接…</p></div>
    </div>
    <div class="spacer"></div>
    <div class="tools">
      <span id="statepill" class="pill" title="运行状态">—</span>
      <span id="clock" class="clock"></span>
      <label class="sw" title="30 秒自动刷新；输入框聚焦或页面隐藏时自动暂停"><input type="checkbox" id="auto" checked>自动刷新</label>
      <button class="btn pri" id="refresh" type="button">刷新</button>
    </div>
  </div>
  <nav class="tabs" id="tabs" role="tablist" aria-label="数据分页"></nav>
</header>
<main id="view" role="tabpanel" aria-live="polite"></main>
<div class="toast" id="toast" role="status" aria-live="polite"></div>
<script>
"use strict";
var TOKEN="";
(function(){
  var q=new URLSearchParams(location.search), t=q.get("token");
  try{ if(t){localStorage.setItem("convo_token",t);} else {t=localStorage.getItem("convo_token")||"";} }catch(e){}
  TOKEN=t||"";
})();

// 页面要什么 —— dashboard_page_test.go 拿这份清单和 server.go 实际注册的路由逐条对账。
var SPEC=[
  ["status","/status"],
  ["inbox","/inbox"],
  ["observation","/observation"],
  ["bias","/bias"],
  ["ownermult","/owner_mult"],
  ["chime","/chime"],
  ["traces","/traces"],
  ["sparks","/sparks"],
  ["learned","/learned"],
  ["fbound","/feedback-boundaries"],
  ["peers","/night-peers"],
  ["memories","/memories"],
  ["boundaries","/boundaries"],
  ["profile","/profile"],
  ["schedule","/schedule"],
  ["dayplan","/dayplan"],
  ["world","/world"],
  ["stickers","/stickers"],
  ["slang","/slang"],
  // 2026-10-16: 时序 / 热力流水 / 关系两层 / 内部状态。
  // ⚠️ 它们原来要么没有出口(heat 早就有端点却没人读), 要么只有一层关系量 ——
  // 于是"群里火热私聊疏离"在页面上**看不出来**。
  ["msgs","/series?kind=msgs&days=30"],
  ["counters","/series?kind=counters&days=30"],
  ["heatlog","/heat?limit=120"],
  ["bonds","/bonds"],
  ["selfactions","/self-actions"],
  ["pokes","/pokes"],
  ["state","/state"]
];
var TABS=[["overview","概览"],["world","世界观"],["persona","人格"],["dialog","对话"],
  ["memory","记忆"],["learn","学习"],["social","社交"],["sticker","表情包"],["slang","黑话"],
  ["state","状态"]];
var DATA={}, ERR={}, TAB="overview", TIMER=null, BUSY=false;
var UI={tq:"",tlimit:30,toff:0,total:0};

function el(id){return document.getElementById(id);}
function esc(s){
  var D=String.fromCharCode(34), S=String.fromCharCode(39);
  return String(s==null?"":s).split("&").join("&amp;").split("<").join("&lt;")
    .split(">").join("&gt;").split(D).join("&quot;").split(S).join("&#39;");
}
function url(p){var sep=p.indexOf("?")>=0?"&":"?";return "/api/v1/convo"+p+(TOKEN?sep+"token="+encodeURIComponent(TOKEN):"");}
function api(p,opt){
  return fetch(url(p),opt||{}).then(function(r){
    if(!r.ok){return r.text().then(function(t){throw new Error("HTTP "+r.status+(t?" "+t.slice(0,140):""));});}
    return r.json();
  });
}
function toast(msg,bad){
  var d=el("toast");d.textContent=msg;d.className="toast on"+(bad?" bad":"");
  clearTimeout(window.__tt);window.__tt=setTimeout(function(){d.className="toast";},3000);
}
function n(v,d){if(v==null||v==="")return "—";if(typeof v==="number")return v.toFixed(d==null?2:d);return String(v);}
function pct(v){var x=Number(v);if(!isFinite(x))return "—";return (x*100).toFixed(x===0?0:1)+"%";}
function bar(v,cls){var x=Math.max(0,Math.min(1,Number(v)||0));
  return '<div class="bar'+(cls?" "+cls:"")+'"><i style="width:'+Math.round(x*100)+'%"></i></div>';}
function barrow(v,cls,txt){return '<div class="barrow">'+bar(v,cls)+'<span>'+esc(txt)+'</span></div>';}
function kpi(v,label,tip){return '<div class="kpi" title="'+esc(tip||label)+'"><b class="gtext">'+esc(v==null?"—":v)+'</b><span>'+esc(label)+'</span></div>';}
function kpis(a){return '<div class="kpis">'+a.join("")+'</div>';}
function card(title,body,meta,flat,cls){
  return '<section class="card'+(flat?" flat":"")+(cls?" "+cls:"")+'"><header><h2 class="gtext">'+esc(title)+'</h2>'+
    (meta?'<div class="meta">'+meta+'</div>':"")+'</header>'+body+'</section>';
}
function empty(t){return '<p class="empty">'+esc(t||"（还没有数据）")+'</p>';}
function tbl(head,rows){
  if(!rows||!rows.length)return empty();
  return '<div class="tw"><table><thead><tr>'+head.map(function(h){return "<th>"+esc(h)+"</th>";}).join("")+
    "</tr></thead><tbody>"+rows.join("")+"</tbody></table></div>";
}
function chips(list,cls){if(!list||!list.length)return empty();
  return list.map(function(x){return '<span class="chip'+(cls?" "+cls:"")+'">'+esc(x)+"</span>";}).join("");}
function errOf(k){return ERR[k]?'<p class="err">取数失败: '+esc(ERR[k])+"</p>":"";}
function has(k){return !ERR[k]&&DATA[k]!=null;}
function copyFrom(id){var e=el(id);if(!e)return;var t=e.textContent;
  if(navigator.clipboard&&navigator.clipboard.writeText){
    navigator.clipboard.writeText(t).then(function(){toast("已复制 "+t.length+" 字");},function(){toast("复制失败",1);});
  }else{
    var a=document.createElement("textarea");a.value=t;document.body.appendChild(a);a.select();
    try{document.execCommand("copy");toast("已复制");}catch(e2){toast("复制失败",1);}
    document.body.removeChild(a);
  }
}
function toggleRow(id){var e=el(id);if(e)e.style.display=e.style.display==="none"?"table-row":"none";}

// 段位着色: 把 system prompt 按 SegmentPrompt 的区间染色 —— 「它当时看到了什么」要看得出边界。
function promptHTML(prompt,segs){
  if(!prompt)return empty("（这条没留 system prompt）");
  if(!segs||!segs.length)return '<div class="pre">'+esc(prompt)+"</div>";
  var s=segs.slice().sort(function(a,b){return a.start-b.start;});
  var names=[],out="",pos=0,i,g,ci,a,b;
  for(i=0;i<s.length;i++){
    g=s[i];ci=names.indexOf(g.name);
    if(ci<0){names.push(g.name);ci=names.length-1;}
    a=Math.max(pos,g.start);b=Math.max(a,g.end);
    if(a>pos)out+=esc(prompt.slice(pos,a));
    out+='<span class="sg sg'+(ci%8)+'" title="'+esc(g.name)+'">'+esc(prompt.slice(a,b))+"</span>";
    pos=Math.max(pos,b);
  }
  if(pos<prompt.length)out+=esc(prompt.slice(pos));
  var legend=names.map(function(nm,i2){return '<span class="chip sg'+(i2%8)+'">'+esc(nm)+"</span>";}).join("");
  return '<div class="pre">'+out+'</div><div class="hint">段位图例 '+legend+"</div>";
}
function relLabel(r){return {companion:"同伴",rival:"对手",family:"家人",mentor:"师长"}[r]||r||"?";}
function slotColor(s){return {sleep:"#475569",free:"#3b82f6",slack:"#34d399",busy:"#fbbf24",focus:"#a78bfa"}[s]||"#64748b";}
// ───────────── 概览 ─────────────
// ——— 图表: 手写 SVG / CSS (零外部依赖) ———
// ⚠️ 这一段里**没有反引号** —— 整页是 Go 的 raw string, 模板字符串会把它当场截断。
// ⚠️ 图表只做一件事: 把已经算好的读数画出来。判断留在 Go 侧, 页面不做推断。
function chBox(w,h,inner){return '<svg class="ch" viewBox="0 0 '+w+' '+h+'" role="img">'+inner+"</svg>";}
function chSeriesLen(series){var m=0;series.forEach(function(s){m=Math.max(m,(s.values||[]).length);});return m;}
function chMax(series){var m=1;series.forEach(function(s){(s.values||[]).forEach(function(v){m=Math.max(m,Number(v)||0);});});return m;}
function n2(v){var x=Number(v);return isFinite(x)?(Math.round(x*10)/10):"—";}
function chLine(series,opt){
  opt=opt||{};
  var w=opt.w||100,h=opt.h||32,pad=2.2,n=Math.max(1,chSeriesLen(series)-1),mx=chMax(series),out="",j;
  for(j=0;j<3;j++){var yy=pad+(j/2)*(h-pad*2);
    out+='<line class="gl" x1="'+pad+'" y1="'+yy.toFixed(2)+'" x2="'+(w-pad)+'" y2="'+yy.toFixed(2)+'"/>';}
  function X(i){return pad+(i/n)*(w-pad*2);}
  function Y(v){return h-pad-(Math.max(0,Number(v)||0)/mx)*(h-pad*2);}
  series.forEach(function(s){
    var vals=s.values||[];if(!vals.length)return;
    var col=s.color||"#a78bfa",pts=[],ar="";
    vals.forEach(function(v,i){pts.push(X(i).toFixed(2)+","+Y(v).toFixed(2));});
    if(series.length<2){
      ar='<polygon class="ar" fill="'+col+'" points="'+X(0).toFixed(2)+","+(h-pad)+" "+pts.join(" ")+" "+
        X(vals.length-1).toFixed(2)+","+(h-pad)+'"/>';
    }
    var last=vals.length-1;
    out+=ar+'<polyline class="ln" points="'+pts.join(" ")+'" stroke="'+col+'"><title>'+esc(s.name||"")+" 最新 "+
      n2(vals[last])+'</title></polyline><circle class="dt" cx="'+X(last).toFixed(2)+'" cy="'+Y(vals[last]).toFixed(2)+
      '" r="1.5"><title>'+esc(s.name||"")+": "+n2(vals[last])+"</title></circle>";
  });
  var lbl="";
  if(opt.labels&&opt.labels.length){
    lbl='<div class="caxis"><span>'+esc(opt.labels[0])+'</span><span>'+
      esc(opt.labels[opt.labels.length-1])+"</span></div>";
  }
  return chBox(w,h,out)+lbl;
}
function chLegend(items){
  if(!items||!items.length)return "";
  return '<div class="legend">'+items.map(function(it){
    return '<span><i style="background:'+it.color+'"></i>'+esc(it.name)+"</span>";}).join("")+"</div>";
}
function chBars(items,opt){
  opt=opt||{};
  if(!items||!items.length)return empty();
  var mx=1;items.forEach(function(it){mx=Math.max(mx,Number(it.value)||0);});
  var out='<div class="cbars">';
  items.forEach(function(it,i){
    var v=Number(it.value)||0,p=Math.max(2,Math.round((v/mx)*100));
    out+='<i title="'+esc(it.label+": "+v)+'" style="height:'+p+"%;animation-delay:"+(i*18)+'ms'+
      (it.color?";background:"+it.color:"")+'></i>';
  });
  out+="</div>";
  if(opt.labels!==false){
    out+='<div class="caxis"><span>'+esc(items[0].label)+'</span><span>'+
      esc(items[items.length-1].label)+"</span></div>";
  }
  return out;
}
function chRing(v,label,color){
  var x=Math.max(0,Math.min(1,Number(v)||0)),off=(264*(1-x)).toFixed(1);
  return '<div class="ring"><svg viewBox="0 0 100 100" role="img">'+
    '<circle cx="50" cy="50" r="42" fill="none" stroke="rgba(255,255,255,.08)" stroke-width="9"/>'+
    '<circle class="rv" cx="50" cy="50" r="42" fill="none" stroke="'+(color||"#a78bfa")+
    '" stroke-width="9" stroke-linecap="round" style="--to:'+off+'"/></svg>'+
    '<div class="ringtx"><b class="gtext">'+pct(x)+"</b><span>"+esc(label||"")+"</span></div></div>";
}
function chHeat(cells,cols){
  if(!cells||!cells.length)return empty();
  var out='<div class="heat" style="grid-template-columns:repeat('+(cols||12)+',1fr)">';
  cells.forEach(function(c,i){
    var v=Math.max(0,Math.min(1,Number(c.v)||0));
    out+='<i title="'+esc(c.title||"")+'" style="background:rgba(167,139,250,'+
      (0.07+v*0.85).toFixed(2)+');animation-delay:'+(i*8)+'ms"></i>';
  });
  return out+"</div>";
}
function chTimeline(items){
  if(!items||!items.length)return empty();
  return '<ul class="tl">'+items.map(function(it){
    return "<li><div>"+esc(it.text)+'</div><div class="t">'+esc(it.when||"")+"</div></li>";}).join("")+"</ul>";
}
// rollNumbers 数字滚动 —— **只滚纯数字**, 且 reduced-motion 下直接不滚。
// ⚠️ 动效关掉时必须是**终值直接可见**, 而不是"从 0 开始但永远滚不完"。
function rollNumbers(root){
  if(!root)return;
  if(window.matchMedia&&window.matchMedia("(prefers-reduced-motion: reduce)").matches)return;
  var els=root.querySelectorAll(".kpi b"),i;
  for(i=0;i<els.length;i++){(function(e){
    var raw=(e.textContent||"").trim();
    if(!/^-?[0-9]+(\.[0-9]+)?$/.test(raw))return;
    var to=parseFloat(raw),dec=(raw.split(".")[1]||"").length,t0=null;
    function step(ts){
      if(t0===null)t0=ts;
      var k=Math.min(1,(ts-t0)/620),v=to*(1-Math.pow(1-k,3));
      e.textContent=v.toFixed(dec);
      if(k<1)requestAnimationFrame(step);else e.textContent=raw;
    }
    requestAnimationFrame(step);
  })(els[i]);}
}

// ——— 状态页 (2026-10-16): 它现在怎么样 / 热力 / 关系两层 / 活动 ———
// ⚠️ 这一页回答的是「它现在到底怎么样」—— 而这些读数原来散在库里**看不见**:
// 心情/身体/睡眠/疲劳/中断/应变有判据没出口, 关系只有一层, 时序一张图都没有。
// ⚠️ 页面**不做推断**: 档位、阶段、阈值全部是 Go 侧给的; 页面只画。
function stageBadge(s){
  var m={stranger:"#94a3b8",acquaint:"#3b82f6",familiar:"#34d399",intimate:"#f472b6"};
  var c=m[s]||"#94a3b8";
  return '<span class="chip" style="color:'+c+'">'+esc(s||"?")+"</span>";
}
// selfKindName / selfStateText 对外动作的两个中文名 (审计那块用)。
function selfKindName(k){
  return ({card:"群名片",avatar:"头像",nickname:"昵称",qzone:"说说"})[k]||String(k||"?");
}
function selfStateText(s){
  return ({applied:"已做",rejected:"它说不要",pending:"等你一个字",failed:"没成"})[s]||String(s||"?");
}

function pState(){
  var s=DATA.state||{}, bd=DATA.bonds||{}, hl=DATA.heatlog||{}, ms=DATA.msgs||{}, cn=DATA.counters||{};
  var mood=s.mood||{}, fx=s.fatigue||{}, ef=s.body_effect||{}, body=s.body||{}, night=s.sleep||{};
  var it=s.interruption||{}, heat=s.heat||[], scn=bd.scene||[], per=bd.person||[], th=bd.thresholds||{};
  var logs=hl.log||[], series=ms.series||[], counters=cn.counters||{};
  var out=[],i,heatP=null;
  for(i=0;i<heat.length;i++){if(!heatP||heat[i].scene===bd.default_scene)heatP=heat[i];}
  // —— 顶部 KPI: 一眼看全 ——
  out.push(kpis([
    kpi(mood.valence==null?"—":n(mood.valence,2),"心情","0 低落 → 1 轻快"),
    kpi(mood.arousal==null?"—":n(mood.arousal,2),"唤醒","低唤醒走「慢/短」; 高唤醒才可能拿到拒绝权"),
    kpi(fx.tier==null?"—":fx.tier,"疲劳档位",fx.reason||""),
    kpi(fx.busy_minutes,"今日忙(分)"),
    kpi(s.strain==null?"—":n(s.strain,2),"应变"),
    kpi(heatP?n(heatP.value,1):"—","私聊热力"),
    kpi(per.length,"人物层记录"),
    kpi(heat.length,"热力场景")
  ]));
  out.push('<div class="grid">');
  // —— 卡 1: 它现在怎么样 ——
  var now=[];
  now.push('<div class="ring2">'+chRing(Number(mood.valence)||0,"心情","#a78bfa")+
    chRing(Number(mood.arousal)||0,"唤醒","#22d3ee")+"</div>");
  if(body.active){
    now.push(barrow(Math.min(1,Number(body.severity)||0),"","身体 "+esc(body.kind||"")+
      (body.cause?" · "+esc(body.cause):"")+" (严重度 "+n(body.severity,2)+")"));
  }else{
    now.push('<p class="empty">身体: 现在没有不适</p>');
  }
  if(ef.DelayFactor){
    now.push(barrow(Math.min(1,(Number(ef.DelayFactor)-1)/2),"","回复延迟 ×"+n(ef.DelayFactor,2)+
      (ef.CanDecline?" · 这一轮可以拒绝":"")+" · 长度 ×"+n(ef.LengthFactor,2)));
  }
  if(night.SleepStartTS){
    now.push('<div class="stat"><b class="gtext">'+esc(String(night.SleepStartTS).slice(11,16))+" → "+
      esc(String(night.WakeTS||"").slice(11,16))+'</b><span>昨晚</span></div>');
    var sc=[];
    if(night.LatencyMin)sc.push("入睡 "+night.LatencyMin+" 分");
    if(night.Interrupts)sc.push("醒 "+night.Interrupts+" 次");
    if(night.Nightmare)sc.push("噩梦");
    if(night.Insomnia)sc.push("失眠");
    if(sc.length)now.push(chips(sc));
  }else{
    now.push('<p class="empty">还没有昨晚的记录</p>');
  }
  if(it.reason){
    now.push(chTimeline([
      {text:"说了一句「"+it.reason+"」",when:it.anchor_ts||""},
      {text:(it.due?"到点了 —— 该接着说":"还挂着 (没到点就不提)"),when:it.due_ts||""}
    ]));
  }else{
    now.push('<p class="empty">没有挂起的中断</p>');
  }
  out.push(card("它现在怎么样",now.join(""),errOf("state"),false,""));
  // —— 卡 2: 热力 ——
  var hb=[];
  if(heat.length){
    heat.forEach(function(h){
      var cap=Number(h.cap_at_settle)||0,v=Number(h.value)||0;
      hb.push(barrow(cap>0?Math.min(1,v/cap):0,"",esc(h.scene)+"  "+n(v,1)+" / "+(cap?n(cap,0):"—")));
    });
    hb.push('<div class="meta" style="margin:4px 0 0;text-align:left">上限是**上次结算时**那一个 (现算需要引擎配置)</div>');
  }else{
    hb.push(empty("还没有热力记录 —— 私聊里说一句就会有一条"));
  }
  if(logs.length){
    hb.push(chLine([{name:"热力值",color:"#a78bfa",values:logs.map(function(r){return Number(r.after)||0;})}],
      {h:34,labels:[String(logs[0].ts||"").slice(5,16),String(logs[logs.length-1].ts||"").slice(5,16)]}));
    hb.push('<div class="meta" style="margin:8px 0 2px;text-align:left">每一笔的增量 (颜色 = 当时的槽位)</div>');
    hb.push(chBars(logs.slice(-40).map(function(r){
      return {label:String(r.ts||"").slice(5,16)+" "+esc(r.slot_state||"?")+" "+esc(r.reason||""),
              value:Number(r.delta)||0,color:slotColor(r.slot_state)};
    })));
  }
  out.push(card("热力 · 每条主动消息 25 点",hb.join(""),errOf("heatlog"),false,""));
  // —— 卡 3: 关系两层 ——
  var rb=[];
  rb.push('<div class="meta" style="text-align:left">阈值 (来自代码): 熟人 '+n(th.acquaint,2)+
    " · 熟悉 "+n(th.familiar,2)+" · 亲密 "+n(th.intimate,2)+'</div>');
  rb.push('<h3 class="sub">场景层 · 每个场景一行</h3>');
  rb.push(tbl(["场景","形态","亲密度","敬意","熟悉度","阶段"],scn.map(function(r){
    return '<tr><td class="mono">'+esc(r.scene_key)+"</td><td>"+esc(r.kind||"")+"</td><td>"+n(r.closeness,2)+
      "</td><td>"+n(r.regard,2)+"</td><td>"+n(r.familiarity,3)+"</td><td>"+stageBadge(r.stage)+"</td></tr>";
  })));
  rb.push('<h3 class="sub">人物层 · 跨场景按 uin 累积</h3>');
  rb.push(tbl(["uin","亲密度","敬意","熟悉度","阶段"],per.map(function(r){
    return '<tr><td class="mono">'+esc(r.uin)+"</td><td>"+n(r.closeness,2)+"</td><td>"+n(r.regard,2)+
      "</td><td>"+n(r.familiarity,3)+"</td><td>"+stageBadge(r.stage)+"</td></tr>";
  })));
  rb.push('<p class="hint">群与私聊是**同一个人**: 群里聊得多, 人物层涨, 私聊那段关系跟着变熟 —— 而群里只读自己那一行。</p>');
  out.push(card("关系 · 两层",rb.join(""),errOf("bonds"),false,""));
  // —— 卡 4: 活动 (30 天) ——
  var ab=[];
  if(series.length){
    ab.push(chLine([
      {name:"ta 说的",color:"#22d3ee",values:series.map(function(d){return Number(d.user)||0;})},
      {name:"它说的",color:"#a78bfa",values:series.map(function(d){return Number(d.ai)||0;})}
    ],{h:34,labels:[series[0].day,series[series.length-1].day]}));
    ab.push(chLegend([{name:"ta 说的",color:"#22d3ee"},{name:"它说的",color:"#a78bfa"}]));
  }else{
    ab.push(empty("这 30 天还没有消息"));
  }
  var opens=counters.ai_opens||[],rounds=counters.rounds||[];
  if(opens.length){
    ab.push('<h3 class="sub">每日主动开口</h3>');
    ab.push(chBars(opens.map(function(d){return {label:d.day,value:Number(d.n)||0};})));
  }
  if(rounds.length){
    ab.push('<h3 class="sub">每日轮次</h3>');
    ab.push(chBars(rounds.map(function(d){return {label:d.day,value:Number(d.n)||0};})));
  }
  out.push(card("活动 · 最近 30 天",ab.join(""),errOf("msgs"),false,""));
  // —— 卡 5: 它自己做过什么 (对外动作的审计) ——
  //
  // 控制权交给它之后, 唯一能让 owner 放心的东西就是"每一次决定都能回看"。
  // 所以这里连它**拒绝**的那些也列出来 —— 拒绝也是一次决定。
  var sb=[];
  var acts=(DATA.selfactions||{}).actions||[];
  if(acts.length){
    sb.push(tbl(["动作","状态","它自己的判断 / 结果","时间"],acts.slice(0,20).map(function(r){
      var when=String(r.applied_ts||r.created_ts||"").slice(5,16);
      return "<tr><td>"+esc(selfKindName(r.kind))+"</td><td>"+esc(selfStateText(r.state))+
        '</td><td>'+esc(String(r.decision||r.result||"—")).slice(0,90)+
        '</td><td class="mono">'+esc(when)+"</td></tr>";
    })));
  }else{
    sb.push(empty("还没有对外动作 —— 这一路默认关着 (self_express.enabled=false)"));
  }
  sb.push('<p class="hint">控制权在它手上, 但每一次决定都留在这里, **包括它拒绝的那些**。</p>');
  var pk=(DATA.pokes||{}).pokes||[];
  if(pk.length){
    sb.push('<h3 class="sub">戳一戳 · 谁戳的、它回没回</h3>');
    sb.push(tbl(["谁","回没回","为什么","时间"],pk.slice(0,12).map(function(r){
      var how=({poke:"戳回去了",line:"搭了句话",ignore:"装死"})[r.replied]||String(r.replied||"?");
      return "<tr><td class=\"mono\">"+esc(r.uin)+"</td><td>"+esc(how)+"</td><td>"+esc(String(r.reason||"").slice(0,40))+
        "</td><td class=\"mono\">"+esc(String(r.ts||"").slice(5,16))+"</td></tr>";
    })));
  }
  out.push(card("它自己做过什么 · 对外动作",sb.join(""),errOf("selfactions"),false,""));
  out.push("</div>");
  return out.join("");
}

function pOverview(){
  var st=DATA.status||{}, ob=DATA.observation||{}, w=DATA.world||{}, b=DATA.bias||{};
  var c=st.counters||{}, stt=ob.stance||{};
  var cast=((w.world||{}).cast)||[], fx=0, dy=0;
  cast.forEach(function(m){ if(m.dynamic)dy++; else fx++; });
  var out=[];
  out.push(kpis([
    kpi(st.state||"—","运行状态"),
    kpi(c.user_msgs,"今日用户消息"),
    kpi(c.rounds,"今日轮次"),
    kpi(c.ai_opens,"今日 AI 开口"),
    kpi((c.ask_share_accept||0)+" / "+(c.ask_share_reject||0),"分享 接受/拒绝"),
    kpi(pct(stt.silent_rate),"沉默率"),
    kpi(((DATA.memories||{}).memories||[]).length,"记忆条目"),
    kpi(((DATA.sparks||{}).sparks||[]).length,"灵光"),
    kpi(fx+" + "+dy,"世界观 定稿/动态"),
    kpi(((DATA.slang||{}).slangs||[]).length,"黑话"),
    kpi(((DATA.stickers||{}).stickers||[]).length,"表情包"),
    kpi(((DATA.peers||{}).peers||[]).length,"夜伴")
  ]));
  out.push('<div class="grid">');
  out.push(card("概况 · 三态与当日配额",
    errOf("observation")+
    kpis([
      kpi(stt.speak,"发言"),kpi(stt.react,"表情"),kpi(stt.silent,"沉默"),
      kpi(pct(stt.silent_rate),"沉默率"),
      kpi((ob.night||{}).ready?"就绪":"缺名册","夜间社交"),
      kpi(((ob.share_week||{}).used||0)+" / "+((ob.share_week||{}).cap||0),"本周分享"),
      kpi(((ob.sparks||{}).sharable||0)+" / "+((ob.sparks||{}).daily_max||0),"可说灵光/日上限"),
      kpi((ob.learned||{}).total||0,"学过的"),
      kpi(pct((ob.learned||{}).unsaid_ratio),"从没说过"),
      kpi((ob.learn||{}).runs_today||0,"今日学习")
    ])+'<div class="hint">三态是硬配额：沉默不是失败，是有配额的正常结局；超配额的那一条会被打回。</div>',"",1));
  out.push(card("今日话题词表 · 夜间批抽的",
    errOf("observation")+chips(ob.topic_keys||[])+
    '<div class="hint">这份词表决定它白天找谁搭话、搭什么话。</div>',"",1));
  out.push(card("运行状态",
    errOf("status")+
    kpis([kpi(st.now||"—","服务时间"),kpi(st.has_profile?"有":"无","人格"),
      kpi((st.schedule||{}).sleep_start||"—","睡点"),kpi((st.schedule||{}).sleep_end||"—","起"),
      kpi((st.recent||[]).length,"近期消息")])+
    '<div class="hint">计数器为「今日」，由注入时钟盖章（时间旅行下也是仿真时间）。</div>',"",1));
  out.push(card("关系倍率 · 主人永远第一", errOf("ownermult")+multBody(DATA.ownermult),
    "上限 "+esc(String((DATA.ownermult||{}).cap||5))+"×",0,"span2"));
  out.push(card("插话收敛 · 被说过就少说", errOf("chime")+chimeBody(DATA.chime),
    esc(String(((DATA.chime||{}).converged||[]).length))+" 个群",0,"span2"));
  out.push(card("偏向可见 · 谁的话被接得多", errOf("bias")+biasBody(b),
    "近 "+esc(b.window_days||14)+" 天",0,"span2"));
  out.push(card("最近消息流", errOf("inbox")+msgBody(DATA.inbox),
    esc(String(((DATA.inbox||{}).msgs||[]).length))+" 条 / 最多 60",0,"span2"));
  out.push("</div>");
  return out.join("");
}
// ⚠️ 倍率是**设计稿 L1301 给 owner 留的杠杆** —— 保证"主人永远第一"。
// 它的作用是加快关系量的积累与抬高私聊的热力上限, 而**不进群场景**(护栏 5)。
// ⚠️ 这张卡是**安全阀的入口** —— chime.go 说「不自动恢复,
// 要放开只能 owner 手动」，而那个口子原来**只出不进**。
//
// ⚠️ 概率**从接口拿**, 不在页面上写死 10/1 —— 写死会与代码常量分叉。
function chimeBody(d){
  d=d||{}; var rows=(d.converged||[]).map(function(x){
    return "<tr><td class=mono>"+esc(x.scene_key)+"</td>"+
      "<td class=mono>"+esc(String(x.complained_ts||"").slice(0,16))+"</td>"+
      "<td>"+esc(String(d.rate_after_pct||1))+"% → "+esc(String(d.rate_pct||10))+"%</td>"+
      '<td><button class="btn sm" onclick="chimeClear(&#39;'+esc(x.scene_key)+"&#39;)'>放行</button></td></tr>";
  });
  return (rows.length
    ? tbl(["场景","被说的时间","放行后概率",""],rows)
    : empty("没有被说过 —— 全部按 "+esc(String(d.rate_pct||10))+"% 走"))+
    '<div class="hint">⚠️ 被群友说过之后它**稳定在 '+esc(String(d.rate_after_pct||1))+'%** 且**不自动恢复** ——'+
    '「重启之后又变得话多」正是群友最会注意到的反复，所以收敛是**落库**的。'+
    '而**放行**是这个功能唯一的安全阀：判定只要求「命中抱怨词 + 这句话在跟它说话」，'+
    '一句对着**别人**说的「别刷屏」也可能落进来。</div>';
}
function chimeClear(scene){
  if(!confirm("放行 "+scene+"？它会回到正常插话概率。"))return;
  api("/chime",{method:"POST",headers:{"Content-Type":"application/json"},
    body:JSON.stringify({scene_key:String(scene)})}).then(function(r){
      toast(r.cleared?("已放行 "+r.scene_key):(r.scene_key+" 本来就没被抱怨过"), !r.cleared);
      return loadAll();
    }).catch(function(e){toast("失败: "+e.message,1);});
}

function multBody(d){
  d=d||{}; var rows=(d.mults||[]).map(function(x){
    // ⚠️ 上限与默认值都**从接口拿**, 不在这里写死 ——
    // 页面上硬编码一个 5 会与代码里的常量分叉, 而分叉之后没人会去比这两处。
    return "<tr><td class=mono>"+esc(x.uin)+"</td>"+
      "<td>"+barrow((Number(x.mult)||1)/Number(d.cap||5),"",esc(String(x.mult))+"×")+"</td>"+
      "<td class=mono>"+esc(String(x.updated||"").slice(0,16))+"</td>"+
      '<td><button class="btn sm" onclick="multSet(&#39;'+esc(x.uin)+"&#39;)'>改</button></td></tr>";
  });
  var add='<div class="row" style="margin-top:10px">'+
    '<input id="multUin" placeholder="uin" style="width:180px">'+
    '<input id="multVal" type="number" step="0.5" min="1" max="'+esc(String(d.cap||5))+'" value="'+esc(String(d.default||1))+'" style="width:90px">'+
    '<button class="btn sm" onclick="multSet()">设</button></div>';
  return (rows.length?tbl(["uin","倍率","更新",""],rows):empty("还没有设过倍率 —— 全部按 1× 走"))+add+
    '<div class="hint">倍率**只加快关系量的积累**，不直接设定它（护栏 1：不许出现「昨天还陌生今天很热」）。'+
    '下限 1×（不许拿来打压某个号），上限 '+esc(String(d.cap||5))+'× —— 调成「舔狗模式」那不叫偏爱，叫没有人格。'+
    '⚠️ **倍率不进群场景**（护栏 5）：否则"有 owner 的群它更话多"会变成一个可观测的统计差别，等于把关系公示出去。</div>';
}
function multSet(uin){
  var u=uin||(el("multUin")||{}).value||"";
  var v=Number((el("multVal")||{}).value||1);
  if(!u){toast("要填 uin",1);return;}
  api("/owner_mult",{method:"POST",headers:{"Content-Type":"application/json"},
    body:JSON.stringify({uin:String(u),mult:v})}).then(function(r){
      // ⚠️ 回显的是**生效值**: 设 8 会变成 5, 而页面必须说出来。
      toast((r.clamped?("已夹到 "+r.mult+"×（你要的是 "+r.requested+"×）"):("已设为 "+r.mult+"×")));
      return loadAll();
    }).catch(function(e){toast("失败: "+e.message,1);});
}

function biasBody(b){
  var rows=(b.speakers||[]).map(function(s){
    var tag=s.cold_shoulder?'<span class="pill bad">冷落</span>':(s.favored?'<span class="pill v">偏向</span>':"");
    return "<tr><td>"+esc(s.uin)+" "+tag+"</td><td>"+esc(s.total)+"</td><td>"+esc(s.spoke)+"</td><td>"+esc(s.reacted)+
      "</td><td>"+esc(s.silent)+"</td><td>"+barrow(s.speak_rate,"",pct(s.speak_rate))+"</td><td>"+
      (s.sample_sufficient?"":'<span class="hint">样本不足</span>')+"</td></tr>";
  });
  var body=rows.length?tbl(["发言人","总","开口","表情","沉默","接话率",""],rows):empty("还没有群决策记录");
  var rs=(b.reasons||[]).slice(0,10).map(function(r){return '<span class="chip">'+esc(r.reason)+" ×"+esc(r.n)+"</span>";}).join("");
  if(rs)body+='<div class="hint">裁决理由 '+rs+"</div>";
  body+='<div class="hint">「一个看不见偏向的群友，比一个没有偏向的群友更危险。」冷落/偏向都由这条时间窗内的真实接话率算出来。</div>';
  return body;
}
function msgBody(inb){
  var msgs=(inb||{}).msgs||[];
  if(!msgs.length)return empty("还没有消息");
  var rows=msgs.slice(0,40).map(function(m){
    var who=m.SenderUIN?("人 "+esc(m.SenderUIN)):(m.Origin?esc(m.Origin):"AI");
    var dir=m.dir==="ai"?'<span class="pill v">出</span>':'<span class="pill">入</span>';
    return "<tr><td class='mono'>"+esc((m.ts||"").slice(5,19))+"</td><td>"+dir+"</td><td>"+esc(m.kind)+"</td><td>"+
      esc(m.channel||"")+"</td><td>"+who+"</td><td class='mono'>"+esc(m.SceneKey||"")+"</td><td>"+
      esc(String(m.payload||"").slice(0,160))+"</td></tr>";
  });
  return tbl(["时刻","向","类型","通道","谁","场景","payload"],rows)+
    '<div class="hint">「谁」来自 sender_uin。群消息里它是空的，全群就会渲染成同一个人 —— 那正是多主体发言流死掉的样子。</div>';
}

// ───────────── 世界观 ─────────────
function pWorld(){
  var wd=DATA.world||{}, b=wd.world;
  var out=['<div class="grid">'];
  if(!b){
    out.push(card("世界观 · 还没有", errOf("world")+
      '<p class="empty">这颗角色还没有世界观。夜间路径会自己生成一份，也可以现在就重生。</p>'+
      worldActions(),"",1));
    out.push("</div>");
    return out.join("");
  }
  var set=b.setting||{};
  out.push(card("它是谁 · 在世界里的位置",
    errOf("world")+'<div class="quote">'+esc(b.self||"（没写 self）")+"</div>"+
    kpis([kpi(esc(b.updated_ts||"—"),"最后写入"),
      kpi((castN(b,false))+" / 5","定稿配角"),
      kpi((castN(b,true))+" / 8","动态配角")])+
    '<div class="hint">定稿配角 ≥2 人（上限 5），动态配角上限 8，**两类各自限额**：动态升级不会把定稿的人挤出去。'+
    '「最后写入」**不是「什么时候推演出这份世界观的」** —— 每天刷新配角状态也会写这一行，所以它至少每天动一次。'+
    '世界观**只在库里没有的时候**才自动生成，生成之后它自己不重推。</div>'+
    worldActions(),"",1));
  out.push(card("世界设定 · L1", errOf("world")+worldSetting(set),"",1));
  var fixed=[],dyn=[];
  (b.cast||[]).forEach(function(m){ (m.dynamic?dyn:fixed).push(m); });
  out.push(card("定稿配角 · 关系层", errOf("world")+castGrid(fixed),
    fixed.length+" 人",0,"span2"));
  out.push(card("动态配角 · 陌生人升级来的", errOf("world")+
    (dyn.length?castGrid(dyn):empty("还没有人升上来")),dyn.length+" 人",0));
  out.push(card("注入提示词的世界观全文", errOf("world")+
    '<div class="bar2"><button class="btn sm" onclick="copyFrom(\'worldprompt\')">复制全文</button></div>'+
    '<div class="pre" id="worldprompt">'+esc(wd.prompt||"")+"</div>",
    esc(String((wd.prompt||"").length))+" 字",1,"span2"));
  out.push("</div>");
  return out.join("");
}
function castN(b,dyn){var k=0;(b.cast||[]).forEach(function(m){if(!!m.dynamic===dyn)k++;});return k;}
function worldActions(){
  return '<div class="bar2" style="margin-top:14px">'+
    '<button class="btn pri" onclick="worldRegen()">重新生成世界观</button>'+
    '<button class="btn danger" onclick="worldClear()">清空</button></div>'+
    '<div class="hint">换角色之后**必须**重生一份：旧世界观会挂在新角色头上，那比没有更糟。生成最长 180 秒。</div>';
}
function worldSetting(set){
  var groups=[["地点",set.places],["组织",set.orgs],["规则",set.rules],["大事时间线",set.timeline]];
  var any=false,html="";
  groups.forEach(function(g){
    var arr=g[1]||[];
    if(!arr.length)return;
    any=true;
    html+='<div style="margin-bottom:10px"><div class="hint" style="margin:0 0 4px">'+esc(g[0])+"</div>"+
      arr.map(function(f){
        var uns=f.source?false:true;
        return '<span class="chip'+(uns?" unsure":"")+'" title="'+esc(f.source?("出处: "+f.source):"没有出处 = 现编")+'">'+
          esc(f.text)+(uns?" ⚠":"")+"</span>";
      }).join("")+"</div>";
  });
  if(!any)return empty("设定是空的");
  return html+'<div class="hint">标 ⚠ 的是**没有出处**的现编 —— 它不许当成原作设定讲出去。</div>';
}
function castGrid(list){
  if(!list.length)return empty();
  return '<div class="castgrid">'+list.map(function(m){
    return '<div class="cast"><div><span class="nm">'+esc(m.name)+'</span>'+
      '<span class="pill v">'+esc(relLabel(m.relation))+"</span>"+
      (m.source?' <span class="pill" title="'+esc(m.source)+'">有出处</span>':' <span class="pill warn">现编</span>')+
      "</div>"+
      (m.status?'<p class="st"><b>现在：</b>'+esc(m.status)+(m.status_day?' <span class="pill">'+esc(m.status_day)+"</span>":"")+"</p>":'<p class="hint" style="margin:7px 0 0">没写「现在在忙什么」</p>')+
      (m.history?'<p><b>共同历史：</b>'+esc(m.history)+"</p>":"")+
      (m.topics?'<p><b>话题偏好：</b>'+esc(m.topics)+"</p>":"")+
      "</div>";
  }).join("")+"</div>";
}

// ───────────── 人格 ─────────────
function pPersona(){
  var p=(DATA.profile||{}).profile||{};
  var o=p.origin||{};
  var bd=(DATA.boundaries||{}).boundaries||{};
  var out=['<div class="grid">'];
  out.push(card("人格 · 它是谁", errOf("profile")+
    kpis([kpi(p.name||"—","名字"),kpi(p.identity||"—","身份"),kpi(p.template||"—","模板"),
      kpi(p.cadence||"—","话频"),kpi(n(p.emotion_amplitude),"情绪幅度"),kpi(n(p.novelty),"新意")])+
    '<div style="margin-top:12px"><span class="hint">它是怎么来的 </span>'+
    (o.mode?('<span class="pill v">'+esc(o.mode)+"</span> "+esc(o.name||"")+" · "+esc(o.series||"")):"<span class=pill>原创</span>")+"</div>"+
    (o.source_url?'<div class="hint mono" style="word-break:break-all">'+esc(o.source_url)+"</div>":"")+
    '<div class="bar2" style="margin-top:14px"><button class="btn sm" onclick="copyFrom(\'persona-json\')">复制原始 JSON</button></div>'+
    '<div class="pre" id="persona-json" style="max-height:220px">'+esc(JSON.stringify(p,null,1))+"</div>","",1));
  var words=[["口头禅",p.catchphrase],["语气词",p.tone_words],["句长",p.sentence_len],["表情习惯",p.sticker_habit]];
  out.push(card("人格词", errOf("profile")+
    (words.filter(function(w){return w[1];}).length
      ? tbl(["",'值'],words.filter(function(w){return w[1];}).map(function(w){return "<tr><td>"+esc(w[0])+"</td><td>"+esc(w[1])+"</td></tr>";}))
      : empty("人格词是空的 —— 可以让它自己生成"))+
    (p.personality?'<div class="hint" style="margin-top:10px;white-space:pre-wrap;color:var(--ink2)">'+esc(p.personality)+"</div>":""),
    "",1));
  out.push(card("轴 · 数值画像", errOf("profile")+
    kpis([kpi(n(p.blend),"blend"),kpi(n(p.tolerance),"tolerance"),kpi(n(p.ui_mix),"ui_mix")])+
    '<div class="hint">这三个是**概率缩放**，只影响倾向；真正的硬闸在代码里（配额、边界、冷却）。</div>',"",1));
  out.push(card("边界 · 隐私开关", errOf("boundaries")+
    kpis([kpi((bd.share_categories||[]).length,"不分享类别"),
      kpi((bd.ask_blacklist||[]).length,"永不再问"),
      kpi(bd.sensitive_no_memory?"开":"关","敏感不记忆")])+
    ((bd.share_categories||[]).length?'<div class="hint">不分享类别 '+chips(bd.share_categories)+"</div>":"")+
    ((bd.ask_blacklist||[]).length?'<div class="hint">永不再问 '+chips(bd.ask_blacklist)+"</div>":"")+
    "",1));
  out.push("</div>");
  return out.join("");
}

// ───────────── 对话 ─────────────
function pDialog(){
  var t=DATA.traces||{};
  var bar='<div class="bar2">'+
    '<input type="search" id="tq" placeholder="搜理由 / 输入 / 输出 / 场景 / uin" value="'+esc(UI.tq)+'" style="flex:1;min-width:220px" '+
    'onkeydown="if(event.key===&#39;Enter&#39;)tsearch(this.value)">'+
    '<select id="tlim" onchange="tlimit(this.value)">'+
      [15,30,60,100,200].map(function(v){return '<option value="'+v+'"'+(v===UI.tlimit?" selected":"")+">"+v+" 条</option>";}).join("")+
    "</select>"+
    '<button class="btn" onclick="tsearch(el(\'tq\').value)">搜索</button>'+
    (UI.tq?'<button class="btn sm" onclick="tsearch(\'\')">清空</button>':"")+
    '<button class="btn sm" onclick="tpage(-1)"'+(UI.toff<=0?" disabled":"")+">上一页</button>"+
    '<button class="btn sm" onclick="tpage(1)"'+((UI.toff+UI.tlimit)>=(t.total||0)?" disabled":"")+">下一页</button>"+
    "</div>";
  var rows=(t.traces||[]).map(function(x){
    var id=x.id;
    return '<tr class="click" onclick="toggleRow(&#39;det'+id+"&#39;)\"><td class=mono>"+id+"</td><td class=mono>"+
      esc(String(x.ts||"").slice(5,19))+"</td><td>"+esc(x.action_kind)+"</td>"+
      '<td><span class="pill'+(x.stance==="speak"?" v":(x.stance==="silent"?"":" warn"))+'">'+esc(x.stance||"—")+"</span></td>"+
      "<td>"+esc(String(x.reason||"").slice(0,46))+"</td>"+
      '<td class=mono>'+esc(x.prompt_bytes)+"</td><td>"+esc(x.outcome||"")+"</td>"+
      '<td class=mono>'+esc(x.speaker_uin||"")+"</td><td class=mono>"+esc(x.scene_key||"")+"</td></tr>"+
      '<tr id="det'+id+'" style="display:none"><td class="det" colspan="9">'+
        '<div class="hint" style="margin-top:0">输入</div><div class="pre" style="max-height:140px">'+esc(x.user_text||"")+"</div>"+
        '<div class="hint">输出</div><div class="pre" style="max-height:200px">'+esc(x.raw_output||"")+"</div>"+
        '<div class="hint">system prompt（按段位着色）</div>'+promptHTML(x.prompt,x.segments)+
      "</td></tr>";
  });
  var meta="共 "+esc(t.total||0)+" 条 · 本页 "+esc(String((t.traces||[]).length))+
    " · 第 "+esc(String(UI.toff+1))+" 起";
  var body=errOf("traces")+bar+
    (rows.length?tbl(["#","时刻","动作","三态","理由","字节","结局","谁","场景"],rows):empty("没有匹配的决策记录"))+
    '<div class="hint">点任意一行展开：它当时的输入、输出、以及**完整的 system prompt**（按段位着色）。不存 hash 只存长度的话，「它当时到底看到了什么」就永远答不上。</div>';
  return card("决策浏览器 · 它当时看到了什么", body, meta, 1);
}

// ───────────── 记忆 ─────────────
function pMemory(){
  var m=(DATA.memories||{}).memories||[];
  var fb=(DATA.fbound||{}).feedback||[];
  var out=['<div class="grid">'];
  // ⚠️ §7.3 **三个正交维度必须看得见** —— 少一维, owner 看到的就是
  // 「这条记忆莫名其妙不见了」, 而它其实是被场景闸门挡了、或被同槽位的新事实取代了。
  var sensLabel={public:"公共",relation:"关系",private:"私域"};
  function memDims(x){
    var s=sensLabel[x.sensitivity]||(x.sensitivity||"未标");
    var cls=x.sensitivity==="public"?"ok":(x.sensitivity==="private"?"bad":"");
    var where=x.scene_key?(x.scene_key.length>18?x.scene_key.slice(0,18)+"…":x.scene_key):"全局";
    var time=x.superseded_ts?("已被取代 "+String(x.superseded_ts).slice(0,10))
      :(x.valid_until?("到 "+String(x.valid_until).slice(0,10))
      :(x.slot_key?("槽位 "+x.slot_key):"长期"));
    var inj=x.inject_count?("取过 "+x.inject_count+" 次"):"";
    return {sens:'<span class="pill '+cls+'">'+esc(s)+"</span>", where:esc(where), time:esc(time), inj:esc(inj)};
  }
  var rows=m.map(function(x){
    var d=memDims(x);
    return "<tr><td class=mono>"+esc(x.mem_id)+"</td><td>"+esc(x.kind)+"</td><td>"+esc(x.label)+"</td>"+
      "<td>"+esc(String(x.v||"").slice(0,180))+"</td>"+
      '<td><span class="pill'+(x.state==="active"?" ok":"")+'">'+esc(x.state)+"</span></td>"+
      "<td>"+d.sens+"</td><td class=mono>"+d.where+"</td><td class=mono>"+d.time+"</td>"+
      "<td>"+barrow(x.confidence,"",n(x.confidence))+"</td>"+
      "<td class=mono>"+esc(x.access_count)+"</td><td class=mono>"+d.inj+"</td>"+
      "<td class=mono>"+esc(String(x.updated||"").slice(0,10))+"</td>"+
      '<td><button class="btn sm danger" onclick="memDel(&#39;'+esc(x.mem_id)+"&#39;)\">删</button></td></tr>";
  });
  out.push(card("记忆 · 全部可看可删", errOf("memories")+
    (rows.length?tbl(["id","类","标签","内容","状态","敏感度","归属","时间","置信","取用","注入","更新",""],rows):empty("还没有记忆"))+
    '<div class="hint">删是**物理删除**。这是隐私出口：你不想被记的事，不该只是「不再显示」。</div>'+
    '<div class="hint">**三维（§7.3）**：<b>敏感度</b> 公共=谁都能用 / 关系=只在那个场景 / 私域（写入口就丢，不会出现在这里）；'+
    '<b>归属</b> = 这条是"在谁面前"得到的，空 = 全局；<b>时间</b> = 长期 / 有槽位（同槽位新事实会顶掉旧的）/ 已被取代 / 有期限。</div>'+
    '<div class="hint">**注入** 是"检索诱导遗忘"的次数 —— 用过的会降温，所以「取用多但注入少」是正常的，不是坏了。</div>',
    m.length+" 条",1,"span2"));
  var fbRows=fb.map(function(x){
    return '<tr><td><span class="pill '+(x.promoted?"bad":"")+'">'+esc(x.promoted?"硬线":"计数中")+"</span></td>"+
      "<td>"+esc(x.scene_key)+"</td><td>"+esc(x.kind)+"</td><td>"+esc(x.topic||"（无话题）")+"</td>"+
      "<td class=mono>"+esc(x.count)+"</td><td>"+esc(x.boundary||"")+"</td></tr>";
  });
  out.push(card("边界 · 它学会了别碰什么", errOf("fbound")+
    (fbRows.length?tbl(["","场景","类","话题","次数","边界"],fbRows):empty("还没有负反馈"))+
    '<div class="hint">累计 '+(esc((DATA.fbound||{}).promote_threshold||"?"))+' 次同类负反馈才升格为硬线；硬线 14 天后软化、60 天后不再注入 —— 是**降权**，不是永久禁止。</div>',
    fb.length+" 条",1,"span2"));
  out.push("</div>");
  return out.join("");
}

// ───────────── 学习 ─────────────
function pLearn(){
  var sp=(DATA.sparks||{}).sparks||[], th=(DATA.sparks||{}).thresholds||{};
  var ln=(DATA.learned||{}).learned||[];
  var out=['<div class="grid">'];
  var srows=sp.map(function(x){
    return '<tr><td><span class="pill v">'+esc(x.kind)+"</span></td><td>"+esc(String(x.content||"").slice(0,200))+"</td>"+
      "<td>"+barrow(x.heat,"warn",n(x.heat))+"</td><td>"+esc(x.status)+"</td>"+
      "<td>"+(x.said?'<span class="pill ok">说过</span>':'<span class="pill">没说</span>')+"</td>"+
      "<td>"+esc(x.source_kind||"")+"</td></tr>";
  });
  out.push(card("灵光 · 它自己想说的", errOf("sparks")+
    (srows.length?tbl(["类","内容","热度","档","","素材"],srows):empty("还没有灵光"))+
    '<div class="hint">档位阈值 fresh ≥ '+esc(th.fresh_at)+" / warm ≥ "+esc(th.warm_at)+
    " · 半衰期 "+esc(th.half_life_h)+" 小时（**现算，不落库** —— 落库的热度会撒谎）。</div>",
    sp.length+" 条",1,"span2"));
  var lrows=ln.slice(0,80).map(function(x){
    return '<tr><td><span class="pill">'+esc(x.kind)+"</span></td><td>"+esc(x.topic_key)+"</td>"+
      "<td>"+esc(String(x.content||"").slice(0,200))+"</td>"+
      '<td><span class="hint">'+esc(x.origin)+"</span></td><td class=mono>"+esc(x.said_count)+"</td></tr>";
  });
  out.push(card("自主学习 · 它自己弄明白的", errOf("learned")+
    (lrows.length?tbl(["档","主题","内容","来历（它看不到）","带出次数"],lrows):empty("还没学过东西"))+
    '<div class="hint">最右列是「带出次数」——学了一堆但从没说过，就是白学。</div>',
    ln.length+" 条",1,"span2"));
  out.push("</div>");
  return out.join("");
}

// ───────────── 社交 ─────────────
function pSocial(){
  var pd=DATA.peers||{}, p=pd.peers||[];
  // ⚠️ #11: 手工表**不是**"夜里会跟谁聊"。夜里优先用世界观 cast, 手工表只是兜底。
  // 原来这张卡只显示手工表, 于是「名册为空 → 整批跳过」和控制台外的 71 轮同时成立。
  var eff=pd.effective||[], effSrc=pd.effective_source||"none";
  var ws=(DATA.schedule||{}).schedule||{};
  var out=['<div class="grid">'];
  var prows=p.map(function(x){
    return "<tr><td class=mono>"+esc(x.uin)+"</td><td>"+esc(x.name)+"</td><td>"+esc(x.brief)+"</td>"+
      "<td>"+barrow(Math.min(1,(x.weight||0)),"ok",n(x.weight))+"</td></tr>";
  });
  var effRows=eff.map(function(x){return "<tr><td class=mono>"+esc(x.uin)+"</td><td>"+esc(x.name)+"</td><td>"+esc(x.brief)+"</td></tr>";});
  var srcLabel={cast:"世界观 cast（优先）",configured:"配置",manual:"手工表（兜底）",none:"没有"}[effSrc]||effSrc;
  out.push(card("夜伴 · 它晚上跟谁聊", errOf("peers")+
    '<div class="hint">夜里**实际会用**的名单（来源：'+esc(srcLabel)+"，共 "+eff.length+" 人）：</div>"+
    (effRows.length?tbl(["uin","名字","简介"],effRows):empty("没有任何名册 —— 夜间多 AI 社交会整批跳过，且不会有任何报错"))+
    '<div class="hint" style="margin-top:12px">下面这张是 <b>手工兜底表</b>（'+p.length+" 人）—— 只有在世界观 cast 长不出来时才会用到它：</div>"+
    (prows.length?tbl(["uin","名字","简介","权重"],prows):empty("手工表是空的（没关系，只要上面有人）")),
    eff.length+" 人",1,"span2"));
  out.push(card("日程 · 它什么时候有空", errOf("schedule")+
'<div class="bar2"><button class="btn pri" onclick="schedRegen()">重新生成周日程</button></div>'+
    schedBody(ws),"",1,"span2"));
  out.push(dayPlanCard());
  out.push(schedRevCard());
  out.push("</div>");
  return out.join("");
}
// daySleepMin 一天里 state=sleep 的真实分钟数 —— **现算, 不读 sleep_start/sleep_end**。
//
// ⚠️ 那两个字段是**展示用**的, 状态机一个都不认(它只认槽表)。
// 2026-10-15 的生产事故就是: 页面显示「睡点 23:30」, 而周三一整天没有一个 sleep 槽。
// 读数必须**从槽表算出来**, 否则这块面板自己就在撒谎。
function daySleepMin(d){
  var t=0;
  (d.slots||[]).forEach(function(s){
    if(s.state!=="sleep")return;
    var a=hmMin(s.start), b=hmMin(s.end);
    if(b>a)t+=b-a;
  });
  return t;
}
function hmMin(v){
  var m=/^(\d{1,2}):(\d{2})$/.exec(String(v||""));
  if(!m)return -1;
  var h=Number(m[1]), mi=Number(m[2]);
  if(h===24&&mi===0)return 1440;
  if(h>23||mi>59)return -1;
  return h*60+mi;
}
function hmText(min){return Math.floor(min/60)+"h"+("0"+(min%60)).slice(-2)+"m";}
function sleepBadge(d){
  var m=daySleepMin(d);
  return '<span class="pill'+(m<360?" bad":" ok")+'" title="从槽表现算; 设计下限 6 小时">睡 '+hmText(m)+"</span>";
}
function schedBody(ws){
  if(!ws.days||!ws.days.length)return empty("没有日程");
  var names=["sun","mon","tue","wed","thu","fri","sat"];
  var todayName=names[new Date().getDay()];
  var cn={sun:"周日",mon:"周一",tue:"周二",wed:"周三",thu:"周四",fri:"周五",sat:"周六"};
  var html='<div class="kpis" style="margin-bottom:12px">'+kpi(ws.sleep_start||"—","睡点")+kpi(ws.sleep_end||"—","起")+
    kpi((ws.days||[]).length,"天")+"</div>";
  ws.days.forEach(function(d){
    var slots=(d.slots||[]).map(function(s){
      return '<span class="slot" style="border-left-color:'+slotColor(s.state)+'" title="'+esc(s.start+" - "+s.end)+'">'+
        esc(s.state)+" "+esc(s.start)+"</span>";
    }).join("");
    html+='<div class="day'+(d.day===todayName?" today":"")+'"><b>'+esc(cn[d.day]||d.day)+'</b><div class="slots">'+
      sleepBadge(d)+(slots||'<span class="hint">没有槽</span>')+"</div></div>";
  });
  html+='<div class="hint">蓝 free / 绿 slack / 黄 busy / 紫 focus / 灰 sleep。**只有 free 和 slack 才会主动开口** —— 别的状态里它就算有话也压着。</div>';
  html+='<div class="hint">「睡」是**从槽表现算**的，不是读 sleep_start —— 那两个字段只作展示，状态机一个都不认。低于 6 小时标红：那是校验的下界（§4.5.4-3）。</div>';
  return html;
}

// ───────────── 表情包 ─────────────
function pSticker(){
  var s=(DATA.stickers||{}).stickers||[];
  var body=errOf("stickers");
  if(!s.length){body+=empty("还没有表情包");}
  else{
    body+='<div class="sgrid">'+s.map(function(x){
      return '<div class="st'+(x.retired?" retired":"")+'">'+
        '<img loading="lazy" alt="'+esc(String(x.id))+'" src="'+esc(url("/stickers/file/"+x.id))+'">'+
        "<small>#"+esc(x.id)+" · "+esc(x.source||"")+"</small>"+
        "<small>aff "+n(x.affinity)+" · 用过 "+esc(x.use_count)+"</small>"+
        "<small>"+barrow(x.affinity,"ok",n(x.score))+"</small>"+
        (x.retired?'<small class="pill bad">已退役</small>':
          '<button class="btn sm" onclick="stDel('+esc(x.id)+')">退役</button>')+
        "</div>";
    }).join("")+"</div>";
  }
  body+='<div class="hint">退避是软性降权：退役的表情包不再被选中，但仍留在库里可回滚。缩略图走 <span class="mono">/stickers/file/{id}</span>。</div>';
  return card("表情包库",body,s.length+" 个",1);
}

// ───────────── 黑话 ─────────────
function pSlang(){
  var s=(DATA.slang||{}).slangs||[];
  var rows=s.map(function(x){
    var st=x.state==="active"?" ok":(x.state==="retired"?"":" warn");
    return '<tr><td class=mono>'+esc(x.id)+"</td><td><b>"+esc(x.phrase)+"</b></td><td>"+esc(x.meaning)+"</td>"+
      "<td>"+esc(String(x.example||"").slice(0,140))+"</td>"+
      '<td><span class="pill'+(x.source==="user"?" v":"")+'">'+esc(x.source)+"</span></td>"+
      '<td><span class="pill'+st+'">'+esc(x.state)+"</span></td>"+
      "<td>"+barrow(x.confidence,"",n(x.confidence))+"</td><td class=mono>"+esc(x.use_count)+"</td>"+
      "<td class=mono>"+esc(String(x.last_used_ts||"").slice(0,10))+"</td>"+
      '<td><button class="btn sm danger" onclick="slDel('+esc(x.id)+')">删</button></td></tr>';
  });
  return card("黑话表 · 群里学的梗", errOf("slang")+
    (rows.length?tbl(["id","词","意思","例句","来历","状态","置信","用过","最近",""],rows):empty("还没有黑话"))+
    '<div class="hint">来历 user = 用户原话里抓到的（带 source_quote 证据）；web = 网上查的。状态 candidate 的还没被允许说。</div>',
    s.length+" 条",1);
}
// ───────────── 动作 ─────────────
function worldRegen(){
  if(!confirm("重新生成世界观？会用当前人格覆盖掉现有世界观（旧的那份不会保留）。"))return;
  toast("生成中…最长 180 秒，别关页面");
  api("/world",{method:"POST"}).then(function(d){
    if(d&&d.ok===false){toast("生成失败: "+(d.error||"未知"),1);}
    else{toast("世界观已重生成");}
    return loadAll();
  }).catch(function(e){toast("失败: "+e.message,1);});
}
function worldClear(){
  if(!confirm("清空世界观？清空后下一次夜间路径会自己重生一份。"))return;
  api("/world",{method:"DELETE"}).then(function(){toast("已清空");return loadAll();})
    .catch(function(e){toast("失败: "+e.message,1);});
}
function memDel(id){
  if(!confirm("永久删除这条记忆？物理删除，不可恢复。"))return;
  api("/memories/"+encodeURIComponent(id),{method:"DELETE"}).then(function(){toast("已删除 "+id);return loadAll();})
    .catch(function(e){toast("失败: "+e.message,1);});
}
function stDel(id){
  if(!confirm("退役这个表情包？它会不再被选中，但仍留在库里。"))return;
  api("/stickers/"+id,{method:"DELETE"}).then(function(){toast("已退役 #"+id);return loadAll();})
    .catch(function(e){toast("失败: "+e.message,1);});
}
function slDel(id){
  if(!confirm("删除这条黑话？"))return;
  api("/slang/"+id,{method:"DELETE"}).then(function(){toast("已删除");return loadAll();})
    .catch(function(e){toast("失败: "+e.message,1);});
}
function tsearch(q){UI.tq=q||"";UI.toff=0;return loadAll();}
function tlimit(v){UI.tlimit=Number(v)||30;UI.toff=0;return loadAll();}
function tpage(d){UI.toff=Math.max(0,UI.toff+d*UI.tlimit);return loadAll();}

// ───────────── 取数 / 渲染 ─────────────
function tracesPath(){
  var p="/traces?limit="+UI.tlimit+"&offset="+UI.toff;
  if(UI.tq)p+="&q="+encodeURIComponent(UI.tq);
  return p;
}
function loadAll(){
  if(BUSY)return Promise.resolve();
  BUSY=true;
  var rb=el("refresh");if(rb)rb.disabled=true;
  el("clock").textContent=new Date().toLocaleString("zh-CN",{hour12:false});
  var jobs=SPEC.map(function(sp){
    var path=sp[1]==="/traces"?tracesPath():sp[1];
    return api(path).then(function(d){DATA[sp[0]]=d;delete ERR[sp[0]];},
      function(e){ERR[sp[0]]=e.message;});
  });
  return Promise.all(jobs).then(function(){
    BUSY=false;
    if(rb)rb.disabled=false;
    paintHead();
    paint();
  });
}
function paintHead(){
  var st=DATA.status||{};
  var bad=Object.keys(ERR).length;
  var sp=el("statepill");
  sp.textContent=(st.state||"未知")+(bad?" · "+bad+" 项失败":"");
  sp.className="pill"+(bad?" bad":(st.has_profile?" ok":""));
  el("sub").textContent="与 convo 服务直连 · "+SPEC.length+" 路数据 · "+
    new Date().toLocaleTimeString("zh-CN",{hour12:false})+
    (TOKEN?" · token 已记忆":" · 无 token");
}
function paintTabs(){
  var h="";
  TABS.forEach(function(t){
    h+='<button class="tab" role="tab" type="button" data-tab="'+t[0]+'" aria-selected="'+(TAB===t[0])+'" onclick="go(\''+t[0]+'\')">'+esc(t[1])+"</button>";
  });
  el("tabs").innerHTML=h;
}
// ⚠️ 分页状态要落到 URL hash: 否则刷新一下就被弹回「概览」,
// 而你正看着的那一屏往往正是最需要发给别人看的那一屏。
function go(t,noHash){
  TAB=t;
  paintTabs();
  paint();
  if(!noHash){try{history.replaceState(null,"","#"+t);}catch(e){}}
  window.scrollTo({top:0,behavior:"smooth"});
}
function tabExists(t){return TABS.some(function(x){return x[0]===t;});}
window.addEventListener("hashchange",function(){
  var h=(location.hash||"").replace("#","");
  if(h&&h!==TAB&&tabExists(h))go(h,true);
});
// SOFT: 这一轮绘制是不是"刷新"(而不是切页) —— 见 paint 与 .noanim。
var SOFT=false;

function paint(){
  var h="";
  if(TAB==="overview")h=pOverview();
  else if(TAB==="world")h=pWorld();
  else if(TAB==="persona")h=pPersona();
  else if(TAB==="dialog")h=pDialog();
  else if(TAB==="memory")h=pMemory();
  else if(TAB==="learn")h=pLearn();
  else if(TAB==="social")h=pSocial();
  else if(TAB==="sticker")h=pSticker();
  else if(TAB==="slang")h=pSlang();
  else if(TAB==="state")h=pState();
  // 刷新与切页共用这一个绘制函数, 但观感必须不同 (见 .noanim 的注释)。
  // 顺手把滚动位置还回去 —— 30 秒一次的自动刷新不该把你正在看的地方甩掉。
  var v=el("view"), sc=window.scrollY;
  v.className=SOFT?"noanim":"";
  v.innerHTML=h;
  rollNumbers(v);
  if(SOFT){window.scrollTo(0,sc);SOFT=false;}
}
function tick(){
  if(!el("auto").checked)return;
  if(document.hidden)return;
  var a=document.activeElement;
  if(a&&(a.tagName==="INPUT"||a.tagName==="SELECT"||a.tagName==="TEXTAREA"))return;
  SOFT=true;
  loadAll();
}
// schedRevCard §4.5.4-2 —— 日程被**程序**改过什么、为什么。
//
// ⚠️ 这一张卡是"日程开始被自动改写"的配套: 在那之前 week_json 只被人工接口写过,
// 所以"它今天怎么变了我睡点"没有别的查法。
function schedRevCard(){
  var d=DATA.schedule||{}, list=d.revisions||[];
  var body=errOf("schedule");
  if(!list.length){
    body+='<div class="hint">还没有自动改写过 —— 作息自修正、约定插入、改期都会在这里留一条。</div>';
  }else{
    body+=tbl(["时间","来源","动了多少","为什么"],list.map(function(r){
      var delta=r.delta_min?(r.delta_min>0?"+":"")+r.delta_min+" 分钟":"—";
      return "<tr><td class=mono>"+esc(r.ts||"")+"</td><td><span class=\"pill\">"+esc(r.source||"")+"</span></td>"+
        '<td class=mono>'+esc(delta)+"</td><td>"+esc(r.reason||"")+(r.detail?' <span class="hint">'+esc(r.detail)+"</span>":"")+"</td></tr>";
    }));
  }
  return card("日程改过什么 · 它为什么改成这样", body, "", 1, "span2");
}

// dayPlanCard §4.5 层 2 —— 「它今天/明天具体干什么」。
//
// ⚠️ 这一层只在夜里排一次: 排歪了白天没有补救手段, 所以这里必须有**看得见 + 能重排**两个口。
function dayPlanCard(){
  var d=DATA.dayplan||{}, t=d.today||{}, m=d.tomorrow||{};
  var body=errOf("dayplan");
  body+='<div class="bar2"><button class="btn pri" onclick="dayPlanRegen()">重排明天的行程</button></div>';
  body+=planBlock("今天", t);
  body+=planBlock("明天", m);
  body+='<div class="hint">行程是**系统事实**，会整段进提示词（§4.6.2 的边界：日程事实必须给全 —— 糊掉它就会退回「21:46 说刚醒」那个病灶）。来源只有五种：cast 世界观配角 / night 夜间回流 / event 事件池 / self 它自己 / user 用户约定 —— **没有第六种**。落在 sleep 槽里的条目会被直接丢掉。</div>';
  return card("行程 · 它今天/明天具体干什么", body, "", 1, "span2");
}
function planBlock(title, box){
  var p=box.plan||{}, items=p.items||[];
  if(!box.ok||!items.length){
    return '<div class="hint" style="margin:8px 0">'+esc(title)+"：还没有行程（夜里排 —— 排不出来就不排，不编）。</div>";
  }
  var rows=items.map(function(x){
    var who=x.with?esc(x.with):'<span class="hint">自己</span>';
    return "<tr><td class=mono>"+esc(x.at)+"</td><td>"+who+"</td><td>"+esc(x.what)+"</td><td>"+esc(x.place||"")+"</td>"+
      '<td><span class="pill">'+esc(x.from)+"</span></td></tr>";
  });
  return '<div class="hint" style="margin:12px 0 4px">'+esc(title)+" · "+esc(box.day||"")+" · "+items.length+" 条</div>"+
    tbl(["时刻","和谁","干什么","地方","来源"],rows);
}

function dayPlanRegen(){
  if(!confirm("重排明天的行程？会覆盖现在这份（最长 180 秒）。"))return;
  toast("生成中…最长 180 秒");
  api("/dayplan",{method:"POST"}).then(function(d){
    if(d&&d.ok===false){toast("没排出来: "+(d.error||"未知"),1);}
    else{toast("明天已排 "+((d.plan&&d.plan.items)?d.plan.items.length:0)+" 条");}
    return loadAll();
  }).catch(function(e){toast("失败: "+e.message,1);});
}

function schedRegen(){
  if(!confirm("重新生成周日程？会用当前人格覆盖现有日程，最长 180 秒。"))return;
  toast("生成中…最长 180 秒，别关页面");
  api("/schedule",{method:"POST"}).then(function(d){
    if(d&&d.ok===false){toast("生成失败: "+(d.error||"未知"),1);}
    else{toast("周日程已重生成");}
    return loadAll();
  }).catch(function(e){toast("失败: "+e.message,1);});
}

el("refresh").addEventListener("click",function(){SOFT=true;loadAll();});
(function(){var h=(location.hash||"").replace("#","");if(h&&tabExists(h))TAB=h;})();
paintTabs();
loadAll().then(function(){setTimeout(tick,0);});
setInterval(tick,30000);
</script></body></html>`
