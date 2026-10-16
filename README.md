# convo

拟人聊天系统 —— 一个常驻在 QQ 好友列表里的「朋友」进程: 有自己的作息、心情和调度循环,
会主动找你说话、会记你的梗、会发匹配语境的表情包。通道走第三方 OneBot v11 (NapCat),
聊天零 UI, 管理页跑在 HTTP 端点上。

本仓库原为 `VocabLock-v2/backend/internal/convo`, 2026-10-16 拆成独立仓库; 除环境变量
`VOCABLOCK_AI_KEY` 的兼容回退外, 不再依赖主程序。

## 目录

| 路径 | 内容 |
|---|---|
| `cmd/convo` | 常驻进程 (P0): 调度 + QQ 通道 + 管理 Web |
| `cmd/convo-sim` | 1 年时间旅行仿真 —— 注入时钟 + 确定性 fake LLM, 零成本零网络 |
| `cmd/convo-trace` | 决策回放: 把「它为什么这么说」摊成一条因果链 |
| `cmd/convo-verify` | 硬线的可执行断言, 退出码即结论 (0 无 E 级 / 1 有违规 / 2 用法或环境错) |
| `cmd/seeddemo` | 本地演示数据播种 (管理页截图用; 临时工具) |
| `internal/convo` | 核心: 人设 / 记忆 / 日程 / 心情 / 场景 / 表情包 / 管理页 |
| `internal/ai` | LLM 客户端 (对话帧协议) + prompt 模板 + 工具与视觉 |
| `internal/clock` | 可注入时钟 (仿真的地基) |
| `internal/log` | 日志 |

## 跑起来

纯 Go, 无 CGO —— 可直接交叉编译到 Pi。go.mod 要求 Go 1.26.3。

```bash
go build ./...
go run ./cmd/convo                                        # 常驻进程
go run ./cmd/convo -admin-dir internal/convo/admin_dist   # 管理页从目录热加载 (开发用)
```

配置: `-config <path>` 指定, 默认 `configs/convo.yaml`。**该文件不存在时按内置默认值启动** ——
监听 `127.0.0.1:8420`, 数据落 `data/`, DB `convo-trial.db`。API key 只走环境变量
`CONVO_AI_KEY` (兼容 `VOCABLOCK_AI_KEY`), 不写进配置文件。

仿真与核验:

```bash
go run ./cmd/convo-sim                                   # 重跑 1 年仿真, 出 CONVO-SIMULATION.md
go run ./cmd/convo-verify -db data/convo-trial.db        # 硬线断言, 退出码即结论
go run ./cmd/convo-trace  -db data/convo-trial.db -n 5   # 回放最近 5 次决策
```

部署 (Pi / linux-arm64): `scripts/deploy-pi.sh` —— 交叉编译 → 两端 md5 必须相等 → 备份后安装 →
重启 → 再核对**运行中的**那个二进制。这套啰嗦的核对来自 2026-10-14 那次事故: 旧二进制被
「安装」成了新版本, 而日志全绿。

## 文档

- [`CONVO-SIMULATION.md`](CONVO-SIMULATION.md) —— 1 年仿真报告 (seed=42, 365 天, 角色=初音)
- 设计稿 `CONVO-SPEC.md` / 拍板记录 `CONVO-DECISIONS.md` 仍在 `VocabLock-v2` 仓库根目录 (未随代码迁入)
