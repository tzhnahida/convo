package convo

import (
	"context"
	"errors"
	"testing"
)

type countingChatter struct {
	out   string
	err   error
	calls int
}

func (c *countingChatter) Chat(ctx context.Context, system, user string) (string, error) {
	c.calls++
	if c.err != nil {
		return "", c.err
	}
	return c.out, nil
}

func (c *countingChatter) ChatVision(ctx context.Context, m, s, u, p string) (string, error) {
	return "", errors.New("unused")
}

const goodBible = "{\"self\":\"须弥的草神\",\"places\":[{\"text\":\"净善宫\",\"source\":\"原神\"}]," +
	"\"cast\":[{\"name\":\"兰那罗\",\"relation\":\"companion\",\"history\":\"一起待过雨季\",\"status\":\"在种蘑菇\"}," +
	"{\"name\":\"散兵\",\"relation\":\"rival\",\"history\":\"吵过\",\"status\":\"在写论文\"}]}"

func worldEngine(t *testing.T, out string, err error) (*Engine, *countingChatter) {
	t.Helper()
	ch := &countingChatter{out: out, err: err}
	e, _ := groupSpeakEngine(t, ch)
	return e, ch
}

// ⚠️ **本批的核心主张**: 名册是从世界观里长出来的。
func TestRosterFromWorld_GeneratesAndStores(t *testing.T) {
	e, ch := worldEngine(t, goodBible, nil)
	peers := e.rosterFromWorld(context.Background(), &PersonaProfile{Name: "纳西妲"})
	if len(peers) != 2 {
		t.Fatalf("应当长出 2 个人, got %d", len(peers))
	}
	if ch.calls != 1 {
		t.Fatalf("应当只调一次模型, got %d", ch.calls)
	}
	// §2.3: 现编必须落库再用。
	if _, ok, _ := e.Store.LoadWorldBible(e.ownerKey()); !ok {
		t.Fatal("生成了却没有落库 —— 下次会自相矛盾")
	}
}

// ⚠️ **幂等点是库, 不是内存标志** —— 重启之后也不许再生成一次。
func TestRosterFromWorld_SecondCallIsFreeOfLLM(t *testing.T) {
	e, ch := worldEngine(t, goodBible, nil)
	e.rosterFromWorld(context.Background(), &PersonaProfile{Name: "纳西妲"})
	before := ch.calls
	peers := e.rosterFromWorld(context.Background(), &PersonaProfile{Name: "纳西妲"})
	if ch.calls != before {
		t.Fatalf("第二次又调了模型 (%d → %d) —— 世界观该从库里读", before, ch.calls)
	}
	if len(peers) != 2 {
		t.Fatalf("第二次读库也应当有 2 个人, got %d", len(peers))
	}
}

// ⚠️ 生成失败**不缓存失败**: 缓存一个"失败过"的标志,
// 会让一次网络抖动变成永久没有世界观 —— 而现象只是"名册一直是空的"。
func TestRosterFromWorld_FailureIsNotCached(t *testing.T) {
	e, ch := worldEngine(t, "", errors.New("网络抖动"))
	if got := e.rosterFromWorld(context.Background(), &PersonaProfile{Name: "纳西妲"}); len(got) != 0 {
		t.Fatal("失败时不该有名册")
	}
	if _, ok, _ := e.Store.LoadWorldBible(e.ownerKey()); ok {
		t.Fatal("失败不该落库")
	}
	// 第二次应当**再试**。
	ch.err = nil
	ch.out = goodBible
	if got := e.rosterFromWorld(context.Background(), &PersonaProfile{Name: "纳西妲"}); len(got) != 2 {
		t.Fatalf("第二次应当重试成功, got %d", len(got))
	}
}

// ⚠️ 校验不过的世界观**不落库也不用** —— 半个世界观比没有更糟。
func TestRosterFromWorld_InvalidNotStored(t *testing.T) {
	bad := "{\"cast\":[{\"name\":\"只有一个\",\"relation\":\"companion\"}]}"
	e, _ := worldEngine(t, bad, nil)
	if got := e.rosterFromWorld(context.Background(), &PersonaProfile{Name: "纳西妲"}); len(got) != 0 {
		t.Fatal("只有一个人的世界观不该长出名字")
	}
	if _, ok, _ := e.Store.LoadWorldBible(e.ownerKey()); ok {
		t.Fatal("非法世界观落库了")
	}
}

// 没有 Chatter 时不许 panic —— 只该退回手工名册。
func TestRosterFromWorld_NoChatter(t *testing.T) {
	e, _ := groupSpeakEngine(t, &sitCaptureChatter{})
	e.Chatter = nil
	if got := e.rosterFromWorld(context.Background(), &PersonaProfile{Name: "x"}); len(got) != 0 {
		t.Fatal("没有模型时应当返回空")
	}
}
