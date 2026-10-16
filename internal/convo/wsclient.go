package convo

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/tzhnahida/convo/internal/log"
)

// ─────────────────────────────────────────────────────────────
// OneBot v11 的**另一个朝向**
//
// 同一份协议, 桥接方有两种形态:
//   · **反向 WS**(server): 桥接方主动连进我们 (NapCat / LLBot 那一系);
//   · **正向 WS**(client): 桥接方自己就是服务端, 我们连出去 (nimbus 的 BRIDGE_WS_SERVER=1)。
//
// ⚠️ 只实现一种的话, 换一个桥接方就得改代码 —— 而这两者之间的差别**只在谁先发起连接**,
// 事件本身一模一样。所以事件处理只有一份 (OneBotWS.HandleRaw), 两个朝向共用。
// ─────────────────────────────────────────────────────────────

const (
	// wsClientMinBackoff / MaxBackoff 重连退避。
	// ⚠️ 必须有上限: 桥接方没起来时, 无上限的退避会让它几小时才重试一次,
	// 表现是"明明修好了却一直不工作"。
	wsClientMinBackoff = time.Second
	wsClientMaxBackoff = 30 * time.Second
	// wsClientHandshakeTimeout 握手超时 —— 短一点, 卡住要能很快重来。
	wsClientHandshakeTimeout = 10 * time.Second
)

// RedactWSToken 把 URL 里的 access_token 抹掉再打日志。
// ⚠️ 忘了这一步 = 桥接方的 token 进了日志文件 —— 日志是最容易被随手贴出去的东西。
func RedactWSToken(raw string) string {
	i := strings.Index(raw, "access_token=")
	if i < 0 {
		return raw
	}
	rest := raw[i+len("access_token="):]
	end := strings.IndexAny(rest, "&")
	if end < 0 {
		return raw[:i] + "access_token=***"
	}
	return raw[:i] + "access_token=***" + rest[end:]
}

// StartOneBotWSClient 以**客户端**身份连出去读事件 (阻塞直到 ctx 结束)。
func StartOneBotWSClient(ctx context.Context, url string, ws *OneBotWS) error {
	u := strings.TrimSpace(url)
	if u == "" {
		return fmt.Errorf("ws client: 地址为空")
	}
	go func() {
		backoff := wsClientMinBackoff
		for {
			if ctx.Err() != nil {
				return
			}
			connected, err := wsClientOnce(ctx, u, ws)
			if ctx.Err() != nil {
				return
			}
			if connected {
				backoff = wsClientMinBackoff // 连上过就重置 —— 否则断一次之后永远等 30 秒
			}
			if err != nil {
				log.Warn("convo:ws", "client 断开: %v (%s 后重连)", err, backoff)
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			backoff *= 2
			if backoff > wsClientMaxBackoff {
				backoff = wsClientMaxBackoff
			}
		}
	}()
	log.Info("convo:ws", "client 模式: 连接 %s", RedactWSToken(u))
	return nil
}

// wsClientOnce 连一次并读到断开。
// 第一个返回值 = **这次有没有连上过** (用来决定要不要重置退避)。
func wsClientOnce(ctx context.Context, url string, ws *OneBotWS) (bool, error) {
	d := websocket.Dialer{HandshakeTimeout: wsClientHandshakeTimeout}
	conn, resp, err := d.DialContext(ctx, url, nil)
	if err != nil {
		if resp != nil {
			return false, fmt.Errorf("dial: %v (http %d)", err, resp.StatusCode)
		}
		return false, fmt.Errorf("dial: %w", err)
	}
	defer conn.Close()
	log.Info("convo:ws", "client 已连接 %s", RedactWSToken(url))
	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return true, fmt.Errorf("read: %w", err)
		}
		ws.HandleRaw(raw)
	}
}
