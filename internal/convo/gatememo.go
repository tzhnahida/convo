package convo

import "sync"

// gateMemo 最近一次**群裁决**的结果, 供 SpeakInGroup 落到决策轨迹里。
//
// ⚠️ 它是**读数的搬运, 不是第二真源**:
//
//	· 只按场景记最后一次, 不参与任何判断;
//	· 键是 scene_key, 所以并发处理两个群不会互相覆盖;
//	· SpeakInGroup 读到就读, 读不到就是空 —— 轨迹里的 reason 缺失, **不是错误**。
//
// 为什么不做成返回值: SpeakInGroup 的签名被多个既有测试直接调用,
// 为了一个观测字段改签名, 收益远小于代价。
type gateMemo struct {
	mu   sync.Mutex
	last map[string]GroupGateResult
}

func (e *Engine) rememberGate(sceneKey string, res GroupGateResult) {
	if e.gate == nil {
		return
	}
	e.gate.mu.Lock()
	defer e.gate.mu.Unlock()
	if e.gate.last == nil {
		e.gate.last = map[string]GroupGateResult{}
	}
	e.gate.last[sceneKey] = res
}

func (e *Engine) lastGate(sceneKey string) (GroupGateResult, bool) {
	if e.gate == nil {
		return GroupGateResult{}, false
	}
	e.gate.mu.Lock()
	defer e.gate.mu.Unlock()
	r, ok := e.gate.last[sceneKey]
	return r, ok
}
