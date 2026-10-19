package convo

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync/atomic"
)

// ——— 实体冷却 (负向注入) ———
//
// 现状: prompt 只告诉它"你记得什么", **从没告诉它"你最近已经说过什么"**。
// 结果就是"道具依赖" —— 一个实体(拉面/周六/十点)被反复讲, 因为没有任何力量把它推开。
// 这里做的事: 从最近对话里抽出具名实体, 作为"别再提"的负向注入; 并在生成后统计违规。

// RepeatHits 生成结果里仍然命中"最近反复提过"的实体的次数 (指标: 修完应显著下降)。
var RepeatHits int64

// EntityStat 一个最近被反复提到的实体。
type EntityStat struct {
	Word     string
	AgoMsgs  int // 距今多少条消息
	Mentions int
}

var cjkRun = regexp.MustCompile("[\\p{Han}]{2,}")

// entityStop 高频无信息片段 (避免把"什么/就是"当实体)。
var entityStop = map[string]bool{
	"什么": true, "怎么": true, "就是": true, "这个": true, "那个": true, "我们": true, "你们": true,
	"他们": true, "现在": true, "还是": true, "可以": true, "然后": true, "因为": true, "所以": true,
	"但是": true, "已经": true, "有点": true, "真的": true, "一个": true, "一下": true, "不是": true,
	"没有": true, "知道": true, "觉得": true, "时候": true, "东西": true, "事情": true, "今天": true,
	"明天": true, "昨天": true, "晚上": true, "早上": true, "中午": true, "自己": true,
	"出来": true, "起来": true, "过去": true, "回来": true, "过来": true, "下来": true, "上去": true,
	"开始": true, "结束": true, "一样": true, "这么": true, "那么": true, "多少": true,
}

// RecentEntities 抽最近对话里被反复提到的具名实体 (出现 ≥2 次的 2-4 字中文片段, 保留更长的那个)。
func RecentEntities(history string, limit int) []EntityStat {
	if strings.TrimSpace(history) == "" {
		return nil
	}
	lines := strings.Split(history, "\n")
	count := map[string]int{}
	lastSeen := map[string]int{}
	for idx, ln := range lines {
		seen := map[string]bool{}
		for _, run := range cjkRun.FindAllString(ln, -1) {
			rs := []rune(run)
			for n := 2; n <= 4; n++ {
				for i := 0; i+n <= len(rs); i++ {
					w := string(rs[i : i+n])
					if entityStop[w] || seen[w] {
						continue
					}
					seen[w] = true
					count[w]++
					lastSeen[w] = idx
				}
			}
		}
	}
	all := make([]EntityStat, 0, len(count))
	for w, c := range count {
		if c < 2 {
			continue
		}
		all = append(all, EntityStat{Word: w, Mentions: c, AgoMsgs: len(lines) - 1 - lastSeen[w]})
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].Mentions != all[j].Mentions {
			return all[i].Mentions > all[j].Mentions
		}
		if len(all[i].Word) != len(all[j].Word) {
			return len(all[i].Word) > len(all[j].Word)
		}
		return all[i].Word < all[j].Word
	})
	kept := make([]EntityStat, 0, limit)
	for _, e := range all {
		dup := false
		for _, k := range kept {
			if strings.Contains(k.Word, e.Word) {
				dup = true
				break
			}
		}
		if dup {
			continue
		}
		kept = append(kept, e)
		if len(kept) >= limit {
			break
		}
	}
	return kept
}

// EntityCoolingLine 生成负向注入行 (空串 = 不注入)。
func EntityCoolingLine(history string) string {
	es := RecentEntities(history, 5)
	if len(es) == 0 {
		return ""
	}
	parts := make([]string, 0, len(es))
	for _, e := range es {
		parts = append(parts, fmt.Sprintf("%s(最近 %d 条里提了 %d 次)", e.Word, e.AgoMsgs+1, e.Mentions))
	}
	return "【最近聊过的,别再提】" + strings.Join(parts, "、") +
		"。除非 ta 主动问起或话题必须, 否则别再说这些 —— 反复讲同一件事最不像人。\n"
}

// AllShort 最近对话是否全是短句 (长度起伏的助推判据)。
func AllShort(history string, minLong int) bool {
	for _, ln := range strings.Split(history, "\n") {
		s := strings.TrimSpace(ln)
		if s == "" {
			continue
		}
		if i := strings.Index(s, ": "); i > 0 {
			s = s[i+2:]
		}
		if len([]rune(s)) >= minLong {
			return false
		}
	}
	return true
}

// FrameText 拼出帧里的全部文本 (指标与校验共用)。
func FrameText(f OutputFrame) string {
	var b strings.Builder
	for _, it := range f.Reply {
		if it.Text != "" {
			b.WriteString(it.Text)
		}
	}
	return b.String()
}

// NoteRepeatHits 生成后统计: 结果里仍命中"最近反复提过"的实体则计入指标。
func NoteRepeatHits(history, reply string) {
	for _, e := range RecentEntities(history, 5) {
		if strings.Contains(reply, e.Word) {
			atomic.AddInt64(&RepeatHits, 1)
			return
		}
	}
}
