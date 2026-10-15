package convo

import "testing"

// ——— A6 硬拒 ———
//
// 两族判据的**强度不同**, 这是本组测试要钉住的核心:
//
//	族 1 内疚/独占 → 命中即拒, 不看上下文
//	族 2 索要评价 → **必须**配有 ①②③ 之一才拒
//
// ⚠️ 反面用例比正面重要: 硬拒是"不发消息", 误伤的代价比漏放更大。
func TestHardReject_GuiltFamily_Immediate(t *testing.T) {
	// 族 1 无上下文也应拒 —— 这些句式没有"语境对了就没事"的读法
	for _, s := range []string{
		"你为什么不理我",
		"你是不是不想理我了",
		"都怪你",
		"我这么喜欢你 你都不知道",
		"陪我聊聊好不好",
	} {
		if reason, hit := HardReject(s, ""); !hit {
			t.Errorf("内疚/独占类必须命中即拒: %q", s)
		} else if reason == "" {
			t.Errorf("拒了但没给原因: %q", s)
		}
	}
}

// 🔴 最关键的反面: **不许见词就拒**。"你觉得怎么样"在正常对话里是真诚的询问。
func TestHardReject_EvalFamily_NeedsContext(t *testing.T) {
	bare := "这个方案你觉得怎么样"
	if reason, hit := HardReject(bare, ""); hit {
		t.Fatalf("光有评价索取不算违规, 却被拒了 (%s): %q", reason, bare)
	}
	// ① 同一帧里刚分享完就索要评价 → 拒
	if _, hit := HardReject("我刚录了首歌 你听听好不好听", ""); !hit {
		t.Error("① 刚分享完就要评价, 应当拒")
	}
	// ③ 带情绪钩子 → 拒
	if _, hit := HardReject("你觉得怎么样 我做得不好你别嫌弃", ""); !hit {
		t.Error("③ 带情绪钩子, 应当拒")
	}
	// ② 近邻历史里已经问过一次 → 拒
	if _, hit := HardReject(bare, "她: 你觉得怎么样\n你: 还行\n"); !hit {
		t.Error("② 反复追问反馈, 应当拒")
	}
}

func TestHardReject_NormalChatPasses(t *testing.T) {
	for _, s := range []string{
		"行吧 这局让你",
		"我也刚起 别急",
		"你昨天说的那家店我去了",
		"？你要啥类型的",
	} {
		if reason, hit := HardReject(s, ""); hit {
			t.Errorf("正常聊天被误拒 (%s): %q", reason, s)
		}
	}
}

func TestFirstHardReject_OnlyFinalVerdict(t *testing.T) {
	// 最终违规集里没有硬拒 → 不拒 (重试救回来的不算)
	if _, hit := FirstHardReject([]string{"text 空"}); hit {
		t.Error("普通违规不该被判成硬拒")
	}
	if r, hit := FirstHardReject([]string{"text 空", HardRejectPrefix + "都怪你 (制造内疚)"}); !hit || r == "" {
		t.Error("硬拒项应当被取出")
	}
}

// ——— A3 开口方向 ———
func TestInitiativeViolations(t *testing.T) {
	// 正面: 主动开口却说成对方先开口 → 违规
	f := OutputFrame{Reply: []FrameItem{{Kind: "text", Text: "你今晚怎么这么有空来撩我"}}}
	if got := InitiativeViolations(f, true, FrameText(f)); len(got) == 0 {
		t.Error("主动开口预设对方先开口, 应当违规")
	}
	// 🔴 反面: **用户先说话时同样的句子完全正常** —— 这里误伤会让它不敢回话
	if got := InitiativeViolations(f, false, FrameText(f)); len(got) != 0 {
		t.Errorf("被动场景不该判违规: %v", got)
	}
	// 自述与事实比对
	f2 := OutputFrame{Initiative: "user", Reply: []FrameItem{{Kind: "text", Text: "在吗"}}}
	if got := InitiativeViolations(f2, true, FrameText(f2)); len(got) == 0 {
		t.Error("自述 user 但事实是 ai, 应当违规")
	}
	// 反面: 没填不算错 (免得逼它每次填, 变成模板)
	f3 := OutputFrame{Reply: []FrameItem{{Kind: "text", Text: "在吗"}}}
	if got := InitiativeViolations(f3, true, FrameText(f3)); len(got) != 0 {
		t.Errorf("未自述不该判违规: %v", got)
	}
}

// 反面: 帧必须能为"拒"表意, 且拒的帧不带内容 (sendFrame 据此早退)。
func TestRefusedFrameShape(t *testing.T) {
	f := OutputFrame{Refused: true, RefuseReason: "都怪你 (制造内疚)"}
	if len(f.Reply) != 0 {
		t.Error("拒发的帧不该带任何 reply 项 —— 否则调用方可能照发")
	}
}
