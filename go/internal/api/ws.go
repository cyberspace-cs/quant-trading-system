package api

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// ===================== 实时推送：SSE + 手写 WebSocket =====================
//
// 为什么两个都要：
//   - SSE：零依赖、自动重连、天然穿透代理，适合「看板」这种只管收的场景；
//   - WebSocket：双向、可订阅多频道、延迟更低，适合「交易台」这种要发指令的场景。
// 这里手写 RFC6455（握手 + 帧编解码），不引第三方库，保持 go run 可跑。

// ---------- SSE ----------

func (s *Server) hStream(c *Ctx) {
	h := c.W.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	flusher, ok := c.W.(http.Flusher)
	if !ok {
		c.Fail(http.StatusInternalServerError, CodeInternal, "stream unsupported")
		return
	}
	// 首帧立即推送，避免前端空等
	writeSSE(c.W, flusher, s.snapshotEvent())

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-c.R.Context().Done():
			return
		case <-ticker.C:
			writeSSE(c.W, flusher, s.snapshotEvent())
		}
	}
}

type sseEvent struct {
	Name string
	Data []byte
}

func writeSSE(w io.Writer, f http.Flusher, ev sseEvent) {
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Name, ev.Data)
	f.Flush()
}

// snapshotEvent 组装一帧快照：总览 + 盘口（一次推送省往返）
func (s *Server) snapshotEvent() sseEvent {
	ov := s.State.Overview()
	ticks := s.State.MarketList()
	payload := map[string]any{"overview": ov, "ticks": ticks, "server_ts": time.Now().UnixMilli()}
	b, _ := json.Marshal(payload)
	return sseEvent{Name: "snapshot", Data: b}
}

// ---------- WebSocket ----------

const wsGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

func wsAccept(key string) string {
	h := sha1.New()
	h.Write([]byte(key + wsGUID))
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

const (
	opCont   = 0x0
	opText   = 0x1
	opBinary = 0x2
	opClose  = 0x8
	opPing   = 0x9
	opPong   = 0xA
)

// hWS WebSocket 入口：?channels=overview,tick 或连接后发送 subscribe 指令
func (s *Server) hWS(c *Ctx) {
	if !strings.EqualFold(c.R.Header.Get("Upgrade"), "websocket") {
		c.Bad("需要 WebSocket 升级请求")
		return
	}
	hj, ok := c.W.(http.Hijacker)
	if !ok {
		c.Fail(http.StatusInternalServerError, CodeInternal, "hijack unsupported")
		return
	}
	conn, rw, err := hj.Hijack()
	if err != nil {
		c.Fail(http.StatusInternalServerError, CodeInternal, "hijack failed: "+err.Error())
		return
	}
	defer conn.Close()

	key := c.R.Header.Get("Sec-WebSocket-Key")
	_, _ = fmt.Fprintf(rw, "HTTP/1.1 101 Switching Protocols\r\n"+
		"Upgrade: websocket\r\nConnection: Upgrade\r\n"+
		"Sec-WebSocket-Accept: %s\r\n\r\n", wsAccept(key))
	if err := rw.Flush(); err != nil {
		return
	}

	channels := map[string]bool{"overview": true}
	if q := c.R.URL.Query().Get("channels"); q != "" {
		for _, ch := range strings.Split(q, ",") {
			if ch = strings.TrimSpace(ch); ch != "" {
				channels[ch] = true
			}
		}
	}

	var mu sync.Mutex // 保护 channels 与写帧（并发写同一 conn 会串帧）
	write := func(op byte, payload []byte) error {
		mu.Lock()
		defer mu.Unlock()
		return wsWrite(conn, op, payload)
	}
	sendJSON := func(v any) error {
		b, err := json.Marshal(v)
		if err != nil {
			return err
		}
		return write(opText, b)
	}

	// 客户端指令：subscribe / unsubscribe / ping
	go func() {
		for {
			op, payload, err := wsRead(rw.Reader)
			if err != nil {
				_ = conn.Close()
				return
			}
			switch op {
			case opClose:
				_ = conn.Close()
				return
			case opPing:
				_ = write(opPong, payload)
			case opText:
				var cmd struct {
					Op       string   `json:"op"`
					Channels []string `json:"channels"`
				}
				if json.Unmarshal(payload, &cmd) != nil {
					continue
				}
				mu.Lock()
				switch cmd.Op {
				case "subscribe":
					for _, ch := range cmd.Channels {
						channels[ch] = true
					}
				case "unsubscribe":
					for _, ch := range cmd.Channels {
						delete(channels, ch)
					}
				}
				active := make([]string, 0, len(channels))
				for ch := range channels {
					active = append(active, ch)
				}
				mu.Unlock()
				_ = sendJSON(map[string]any{"type": "ack", "op": cmd.Op, "channels": active})
			}
		}
	}()

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	// 首帧
	_ = sendJSON(map[string]any{"type": "hello", "server": "quant-ws", "version": Version, "ts": time.Now().UnixMilli()})

	for range ticker.C {
		mu.Lock()
		subOv, subTick, subRisk := channels["overview"], channels["tick"], channels["risk"]
		mu.Unlock()

		if subOv {
			ov := s.State.Overview()
			if err := sendJSON(map[string]any{"type": "overview", "data": ov, "ts": time.Now().UnixMilli()}); err != nil {
				return
			}
		}
		if subTick {
			if err := sendJSON(map[string]any{"type": "tick", "data": s.State.MarketList(), "ts": time.Now().UnixMilli()}); err != nil {
				return
			}
		}
		if subRisk {
			logs := s.State.RiskLogs(5)
			if err := sendJSON(map[string]any{"type": "risk", "data": logs, "ts": time.Now().UnixMilli()}); err != nil {
				return
			}
		}
	}
}

// wsWrite 写一帧（服务端不掩码）
func wsWrite(w io.Writer, opcode byte, payload []byte) error {
	n := len(payload)
	head := make([]byte, 0, 10)
	head = append(head, 0x80|opcode)
	switch {
	case n < 126:
		head = append(head, byte(n))
	case n < 65536:
		head = append(head, 126, byte(n>>8), byte(n))
	default:
		head = append(head, 127)
		var ext [8]byte
		binary.BigEndian.PutUint64(ext[:], uint64(n))
		head = append(head, ext[:]...)
	}
	if _, err := w.Write(head); err != nil {
		return err
	}
	_, err := w.Write(payload)
	return err
}

// wsRead 读一帧（客户端必须掩码）并解掩码
func wsRead(r *bufio.Reader) (byte, []byte, error) {
	h := make([]byte, 2)
	if _, err := io.ReadFull(r, h); err != nil {
		return 0, nil, err
	}
	op := h[0] & 0x0f
	masked := h[1]&0x80 != 0
	n := int64(h[1] & 0x7f)
	switch n {
	case 126:
		ext := make([]byte, 2)
		if _, err := io.ReadFull(r, ext); err != nil {
			return 0, nil, err
		}
		n = int64(binary.BigEndian.Uint16(ext))
	case 127:
		ext := make([]byte, 8)
		if _, err := io.ReadFull(r, ext); err != nil {
			return 0, nil, err
		}
		n = int64(binary.BigEndian.Uint64(ext))
	}
	if n > 1<<20 {
		return 0, nil, fmt.Errorf("frame too large: %d", n)
	}
	var mask []byte
	if masked {
		mask = make([]byte, 4)
		if _, err := io.ReadFull(r, mask); err != nil {
			return 0, nil, err
		}
	}
	payload := make([]byte, n)
	if n > 0 {
		if _, err := io.ReadFull(r, payload); err != nil {
			return 0, nil, err
		}
	}
	if masked {
		for i := range payload {
			payload[i] ^= mask[i%4]
		}
	}
	return op, payload, nil
}
