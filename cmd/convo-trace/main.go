// convo-trace —— P5 决策回放 (D1)。
//
// "它为什么这次这么说?" 这个问题要横跨五个子系统才答得上: 状态事实 / 心情 / 注入的记忆 /
// 调度动作 / 帧校验结果。这个命令把它们按时间顺序摊开成一条人能读的因果链。
//
// 用法:
//
//	go run ./cmd/convo-trace -db ../data/convo-trial.db -n 5
//	go run ./cmd/convo-trace -db <path> -n 1 -prompt    # 额外打印完整提示词
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/tzhnahida/VocabLock/internal/convo"
)

func main() {
	db := flag.String("db", "", "convo 数据库路径 (必填)")
	n := flag.Int("n", 5, "回放最近多少条")
	showPrompt := flag.Bool("prompt", false, "额外打印完整提示词 (它当时看到的一切)")
	flag.Parse()

	if *db == "" {
		fmt.Fprintln(os.Stderr, "用法: convo-trace -db <convo.db> [-n 5] [-prompt]")
		os.Exit(2)
	}
	store, err := convo.OpenStore(filepath.Dir(*db), filepath.Base(*db))
	if err != nil {
		fmt.Fprintf(os.Stderr, "打开数据库失败: %v\n", err)
		os.Exit(1)
	}
	defer store.Close()

	traces, err := store.RecentTraces(*n)
	if err != nil {
		fmt.Fprintf(os.Stderr, "读轨迹失败: %v\n", err)
		os.Exit(1)
	}
	if len(traces) == 0 {
		fmt.Println("还没有轨迹。跑一次带对话的仿真或试跑后再来。")
		return
	}

	// 新的在前读着别扭 —— 按时间正序打印, 让因果链是从上往下的。
	for i := len(traces) - 1; i >= 0; i-- {
		t := traces[i]
		fmt.Print(convo.RenderTrace(t))
		if *showPrompt {
			fmt.Println("   ── 提示词全文 ──")
			fmt.Println(t.SystemPrompt)
			fmt.Println("   ── 提示词结束 ──")
		}
		fmt.Println()
	}
}
