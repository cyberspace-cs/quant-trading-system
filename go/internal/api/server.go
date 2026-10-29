package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Server HTTP 接口层
type Server struct {
	State  *AppState
	WebDir string
}

func NewServer(st *AppState, webDir string) *Server {
	return &Server{State: st, WebDir: webDir}
}

// Handler 构建路由：/api/v1/* 走接口，其余走静态文件（前端工作台）
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/", s.route)
	mux.Handle("/", http.FileServer(http.Dir(s.WebDir)))
	return cors(mux)
}

func (s *Server) route(w http.ResponseWriter, r *http.Request) {
	p := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v1/"), "/")
	segs := strings.Split(p, "/")

	switch {
	case p == "health":
		writeJSON(w, 200, map[string]any{"status": "ok", "ts": time.Now().Format(time.RFC3339)})

	case p == "overview":
		writeJSON(w, 200, s.State.Overview())

	case p == "positions":
		writeJSON(w, 200, s.State.Positions())

	case p == "orders":
		writeJSON(w, 200, s.State.Orders(queryLimit(r, 50)))

	case p == "fills":
		writeJSON(w, 200, s.State.Fills(queryLimit(r, 50)))

	case p == "signals":
		writeJSON(w, 200, s.State.SignalList(queryLimit(r, 50)))

	case p == "risk/logs":
		writeJSON(w, 200, s.State.RiskLogs(queryLimit(r, 50)))

	case p == "risk/rules":
		writeJSON(w, 200, s.rules())

	case p == "strategies":
		writeJSON(w, 200, s.State.StrategyList())

	case p == "performance":
		writeJSON(w, 200, s.State.Performance())

	case p == "factors":
		writeJSON(w, 200, s.State.FactorOf(r.URL.Query().Get("symbol")))

	case p == "market/tick":
		writeJSON(w, 200, s.State.MarketList())

	case p == "market/pool":
		writeJSON(w, 200, s.State.Pool())

	case p == "reconcile":
		writeJSON(w, 200, s.State.Reconcile())

	case p == "reconcile/run" && r.Method == http.MethodPost:
		writeJSON(w, 200, s.State.Reconcile())

	case p == "metrics":
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		ov := s.State.Overview()
		fmt.Fprintf(w, "# TYPE quant_nav gauge\nquant_nav %.4f\n", ov.Nav)
		fmt.Fprintf(w, "# TYPE quant_day_pnl gauge\nquant_day_pnl %.2f\n", ov.DayPnL)
		fmt.Fprintf(w, "# TYPE quant_latency_us gauge\nquant_latency_us %d\n", ov.LatencyUs)

	case p == "stream":
		s.stream(w, r)

	case len(segs) == 3 && segs[0] == "strategies" && segs[2] == "switch" && r.Method == http.MethodPost:
		var body struct {
			Status string `json:"status"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if s.State.SwitchStrategy(segs[1], body.Status) {
			writeJSON(w, 200, map[string]any{"ok": true, "id": segs[1], "status": body.Status})
		} else {
			writeJSON(w, 404, map[string]any{"ok": false, "error": "strategy not found"})
		}

	case len(segs) == 3 && segs[0] == "orders" && segs[2] == "cancel" && r.Method == http.MethodPost:
		if s.State.CancelOrder(segs[1]) {
			writeJSON(w, 200, map[string]any{"ok": true, "id": segs[1]})
		} else {
			writeJSON(w, 409, map[string]any{"ok": false, "error": "order not cancellable"})
		}

	default:
		writeJSON(w, 404, map[string]any{"error": "not found", "path": r.URL.Path})
	}
}

// rules 风控规则视图
func (s *Server) rules() []map[string]any {
	e := s.State.PreRisk
	return []map[string]any{
		{"rule_id": "R_POS", "dimension": "position", "threshold": e.MaxPositionRatio, "action": "REJECT", "enabled": true},
		{"rule_id": "R_IND", "dimension": "industry", "threshold": e.MaxIndustryRatio, "action": "REJECT", "enabled": true},
		{"rule_id": "R_LOSS", "dimension": "pnl", "threshold": e.MaxDayLossRatio, "action": "HALT", "enabled": true},
		{"rule_id": "R_DD", "dimension": "drawdown", "threshold": e.MaxDrawdownRatio, "action": "HALT", "enabled": true},
		{"rule_id": "R_LOT", "dimension": "lot", "threshold": float64(e.MaxLot), "action": "REJECT", "enabled": true},
		{"rule_id": "R_TURN", "dimension": "turnover", "threshold": float64(e.MaxTurnover), "action": "REJECT", "enabled": true},
	}
}

// stream SSE 实时推送（生产可替换为 WebSocket）
func (s *Server) stream(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "stream unsupported", 500)
		return
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	// 先发一帧，避免前端空等
	s.writeEvent(w, flusher)
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			s.writeEvent(w, flusher)
		}
	}
}

func (s *Server) writeEvent(w http.ResponseWriter, f http.Flusher) {
	data, _ := json.Marshal(s.State.Overview())
	fmt.Fprintf(w, "event: overview\ndata: %s\n\n", data)
	f.Flush()
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET,POST,PUT,DELETE,OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type,Authorization")
		if r.Method == http.MethodOptions {
			w.WriteHeader(204)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func queryLimit(r *http.Request, def int) int {
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}
