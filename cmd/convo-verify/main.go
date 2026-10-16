// convo-verify —— 硬线的**可执行断言**。
//
// 监理模式下最要紧的一件事: **凡是机器能判的, 绝不靠肉眼。**
// 一条命令把设计稿里"有机制保证"的约束跑一遍, **退出码即结论**:
//
//	0 = 无 E 级问题 (W 级警告不算失败)
//	1 = 存在 E 级违规 (硬线被破坏)
//	2 = 用法错 / 打不开库 (**与违规分开** —— 否则 CI 会把环境问题当违规)
//
// 用法: go run ./cmd/convo-verify -db /tmp/demo/convo-sim.db
package main

import (
	"os"

	"github.com/tzhnahida/convo/internal/convo"
)

func main() {
	os.Exit(convo.VerifyMain(os.Args[1:], os.Stdout))
}
