package api

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"quant/internal/common"
	"quant/internal/execution"
)

// ===================== 账户 / 持仓 / 订单 / 行情 =====================

func (s *Server) hOverview(c *Ctx) { c.OK(s.State.Overview()) }

func (s *Server) hPositions(c *Ctx) {
	all := s.State.Positions()
	p := ParsePage(c, 200)
	q := p.Filter
	rows := make([]PositionView, 0, len(all))
	for _, r := range all {
		if Match(q, r.Symbol, r.Industry) {
			rows = append(rows, r)
		}
	}
	switch p.Sort {
	case "pnl":
		sort.SliceStable(rows, func(i, j int) bool {
			if p.Order == "asc" {
				return rows[i].PnL < rows[j].PnL
			}
			return rows[i].PnL > rows[j].PnL
		})
	case "symbol":
		SortBy(rows, func(r PositionView) string { return r.Symbol }, p.Order == "desc")
	default:
		sort.SliceStable(rows, func(i, j int) bool { return rows[i].MarketValue > rows[j].MarketValue })
	}
	page, meta := Slice(rows, p)
	c.OKPage(page, meta)
}

func (s *Server) hPerformance(c *Ctx) { c.OK(s.State.Performance()) }

// ---------- 订单查询 ----------

func (s *Server) hOrders(c *Ctx) {
	p := ParsePage(c, 50)
	status := strings.ToUpper(c.Query("status"))
	symbol := c.Query("symbol")
	strategy := c.Query("strategy")

	all := s.State.Book.Orders()
	rows := make([]OrderView, 0, len(all))
	for _, o := range all {
		if status != "" && common.StatusName(o.Status) != status {
			continue
		}
		if symbol != "" && o.Symbol != symbol {
			continue
		}
		if strategy != "" && o.StrategyID != strategy {
			continue
		}
		if !Match(p.Filter, o.OrderID, o.Symbol, o.StrategyID, common.StatusName(o.Status)) {
			continue
		}
		if o.ParentID != "" && c.Query("include_children") != "1" {
			continue // 默认只列母单，避免拆单把列表刷爆
		}
		rows = append(rows, OrderView{
			OrderID: o.OrderID, Symbol: o.Symbol, Side: o.Side.String(),
			Strategy: o.StrategyID, Qty: o.OrigQty, Filled: o.FilledQty,
			Price: round2(o.Price), Status: common.StatusName(o.Status),
			Time: time.UnixMilli(o.CreatedAt).Format("15:04:05"),
		})
	}
	switch p.Sort {
	case "qty":
		sort.SliceStable(rows, func(i, j int) bool {
			if p.Order == "asc" {
				return rows[i].Qty < rows[j].Qty
			}
			return rows[i].Qty > rows[j].Qty
		})
	case "symbol":
		SortBy(rows, func(r OrderView) string { return r.Symbol }, p.Order == "desc")
	}
	page, meta := Slice(rows, p)
	c.OKPage(page, meta)
}

// OrderDetail 订单详情
type OrderDetail struct {
	Order     OrderView     `json:"order"`
	Children  []OrderView   `json:"children"`
	Fills     []FillView    `json:"fills"`
	Risk      []RiskLogView `json:"risk_checks"`
	Amendable bool          `json:"amendable"`
}

func (s *Server) hOrderDetail(c *Ctx) {
	id := c.Path("id")
	o, ok := s.State.Book.Order(id)
	if !ok {
		c.NotFound("订单不存在：" + id)
		return
	}
	d := OrderDetail{
		Order: OrderView{
			OrderID: o.OrderID, Symbol: o.Symbol, Side: o.Side.String(), Strategy: o.StrategyID,
			Qty: o.OrigQty, Filled: o.FilledQty, Price: round2(o.Price),
			Status: common.StatusName(o.Status), Time: time.UnixMilli(o.CreatedAt).Format("15:04:05"),
		},
		Children: []OrderView{}, Fills: []FillView{}, Risk: []RiskLogView{},
		Amendable: o.Status == common.Sent || o.Status == common.Partial,
	}
	for _, ch := range s.State.Book.OrdersByParent(id) {
		d.Children = append(d.Children, OrderView{
			OrderID: ch.OrderID, Symbol: ch.Symbol, Side: ch.Side.String(), Strategy: ch.StrategyID,
			Qty: ch.OrigQty, Filled: ch.FilledQty, Price: round2(ch.Price),
			Status: common.StatusName(ch.Status), Time: time.UnixMilli(ch.CreatedAt).Format("15:04:05"),
		})
	}
	// 成交挂在子单上，母单详情要把所有子单的成交聚合进来
	ids := map[string]bool{id: true}
	for _, ch := range s.State.Book.OrdersByParent(id) {
		ids[ch.OrderID] = true
	}
	for _, f := range s.State.Book.Fills() {
		if !ids[f.OrderID] {
			continue
		}
		d.Fills = append(d.Fills, FillView{
			FillID: f.FillID, Symbol: f.Symbol, Side: f.Side.String(), Qty: f.FilledQty,
			Price: round2(f.FillPrice), Amount: round2(f.FillAmount),
			Time: time.UnixMilli(f.FillTime).Format("15:04:05"),
		})
	}
	c.OK(d)
}

// ---------- 手动下单 ----------

type createOrderReq struct {
	Symbol     string  `json:"symbol"`
	Side       string  `json:"side"` // BUY / SELL
	Qty        int64   `json:"qty"`
	Price      float64 `json:"price"`
	OrderType  int8    `json:"order_type"` // 1 限价 2 市价
	StrategyID string  `json:"strategy_id"`
	Algo       string  `json:"algo"` // TWAP / VWAP / ADAPTIVE
	Slices     int     `json:"slices"`
	IntervalMs int64   `json:"interval_ms"`
	Reason     string  `json:"reason"`
}

// OrderResult 下单结果
type OrderResult struct {
	Parent    OrderView   `json:"parent"`
	Children  []OrderView `json:"children"`
	Fills     int         `json:"fill_count"`
	RiskPass  bool        `json:"risk_pass"`
	RiskRule  string      `json:"risk_rule,omitempty"`
	RiskMsg   string      `json:"risk_msg,omitempty"`
	LatencyUs int64       `json:"latency_us"`
}

func (s *Server) hCreateOrder(c *Ctx) {
	var req createOrderReq
	if err := c.Bind(&req); err != nil {
		c.Bad("请求体格式错误：" + err.Error())
		return
	}
	if err := s.validateOrder(&req); err != nil {
		c.Bad(err.Error())
		return
	}
	res, code, msg := s.placeOrder(req, c.User.Name)
	if code != 0 {
		if code == CodeRiskRejected {
			c.Fail(http.StatusUnprocessableEntity, CodeRiskRejected, msg)
			return
		}
		c.Conflict(msg)
		return
	}
	c.Act("order.create", "order:"+res.Parent.OrderID,
		fmt.Sprintf("%s %s %d股 @%.2f via %s（%d 子单，风控放行）",
			req.Side, req.Symbol, req.Qty, req.Price, req.Algo, len(res.Children)))
	c.OK(res)
}

// validateOrder 入参校验（接口层，不涉及业务规则）
func (s *Server) validateOrder(req *createOrderReq) error {
	if req.Symbol == "" {
		return fmt.Errorf("symbol 不能为空")
	}
	if req.Side = strings.ToUpper(req.Side); req.Side != "BUY" && req.Side != "SELL" {
		return fmt.Errorf("side 必须是 BUY 或 SELL")
	}
	if req.Qty <= 0 {
		return fmt.Errorf("qty 必须大于 0")
	}
	if req.Qty%100 != 0 {
		return fmt.Errorf("A 股委托数量必须是 100 股的整数倍，当前 %d", req.Qty)
	}
	if req.OrderType == 0 {
		req.OrderType = 1
	}
	if req.OrderType == 1 && req.Price <= 0 {
		// 限价单未给价：用最新价兜底（实盘应拒绝，这里为演示便捷）
		req.Price = s.State.PriceOf(req.Symbol)
		if req.Price <= 0 {
			return fmt.Errorf("限价单必须指定 price（该标的暂无行情）")
		}
	}
	if req.Algo = strings.ToUpper(req.Algo); req.Algo == "" {
		req.Algo = "TWAP"
	}
	if req.Algo != "TWAP" && req.Algo != "VWAP" && req.Algo != "ADAPTIVE" {
		return fmt.Errorf("algo 仅支持 TWAP / VWAP / ADAPTIVE")
	}
	if req.Slices <= 0 {
		req.Slices = 1
	}
	if req.Slices > 20 {
		return fmt.Errorf("slices 上限 20")
	}
	if req.IntervalMs <= 0 {
		req.IntervalMs = 200
	}
	if req.StrategyID == "" {
		req.StrategyID = "manual"
	}
	return nil
}

// placeOrder 下单主流程：事前风控 → 拆单 → OMS 上报 → 聚合母单状态
func (s *Server) placeOrder(req createOrderReq, operator string) (*OrderResult, int, string) {
	start := time.Now()
	side := common.Buy
	if req.Side == "SELL" {
		side = common.Sell
	}
	sig := common.Signal{
		StrategyID: req.StrategyID, Symbol: req.Symbol, Side: side,
		Qty: req.Qty, Price: req.Price, Ts: common.NowMs(),
	}

	// 1) 事前风控：违规直接丢弃，绝不进柜台
	ctx := s.State.Snapshot()
	if ok, reason := s.State.PreRisk.Check(sig, ctx); !ok {
		return nil, CodeRiskRejected, "事前风控拦截：" + reason
	}

	// 2) 生成母单（序号必须来自原子计数器：同一毫秒内并发下单不能撞号）
	now := common.NowMs()
	seq := atomic.AddInt64(&s.ordSeq, 1)
	parent := common.Order{
		OrderID:   fmt.Sprintf("ORD-%s-%05d", time.Now().Format("150405"), seq),
		AccountID: s.State.AccountID, StrategyID: req.StrategyID, Symbol: req.Symbol,
		Side: side, OrderType: req.OrderType, OrigQty: req.Qty, Price: req.Price,
		Status: common.Sent, CreatedAt: now, UpdatedAt: now,
	}

	// 3) 智能拆单：大单切碎，降低冲击成本
	algo := execution.TWAP
	switch req.Algo {
	case "VWAP":
		algo = execution.VWAP
	case "ADAPTIVE":
		algo = execution.Adaptive
	}
	children := execution.Slice(parent, algo, req.Slices, req.IntervalMs, now)
	fillCount := 0
	var filledSum int64
	views := make([]OrderView, 0, len(children))
	for i := range children {
		ch := children[i]
		fills := s.State.OMS.Submit(&ch)
		fillCount += len(fills)
		filledSum += ch.FilledQty
		views = append(views, OrderView{
			OrderID: ch.OrderID, Symbol: ch.Symbol, Side: ch.Side.String(), Strategy: ch.StrategyID,
			Qty: ch.OrigQty, Filled: ch.FilledQty, Price: round2(ch.Price),
			Status: common.StatusName(ch.Status), Time: time.UnixMilli(ch.CreatedAt).Format("15:04:05"),
		})
	}
	// 4) 聚合母单状态（子单全部成交 → 母单成交）
	if filledSum >= parent.OrigQty {
		parent.Status = common.Filled
	} else if filledSum > 0 {
		parent.Status = common.Partial
	}
	parent.FilledQty = filledSum
	s.State.Book.SaveOrder(&parent)

	return &OrderResult{
		Parent: OrderView{
			OrderID: parent.OrderID, Symbol: parent.Symbol, Side: parent.Side.String(),
			Strategy: parent.StrategyID, Qty: parent.OrigQty, Filled: parent.FilledQty,
			Price: round2(parent.Price), Status: common.StatusName(parent.Status),
			Time: time.UnixMilli(parent.CreatedAt).Format("15:04:05"),
		},
		Children: views, Fills: fillCount, RiskPass: true,
		LatencyUs: time.Since(start).Microseconds(),
	}, 0, ""
}

// hAmendOrder 改单
func (s *Server) hAmendOrder(c *Ctx) {
	id := c.Path("id")
	var req struct {
		Qty    int64   `json:"qty"`
		Price  float64 `json:"price"`
		Reason string  `json:"reason"`
	}
	if err := c.Bind(&req); err != nil {
		c.Bad("请求体格式错误：" + err.Error())
		return
	}
	if req.Qty <= 0 && req.Price <= 0 {
		c.Bad("qty 与 price 至少提供一个")
		return
	}
	if req.Qty%100 != 0 && req.Qty > 0 {
		c.Bad("A 股委托数量必须是 100 股的整数倍")
		return
	}
	o, msg := s.State.Book.AmendOrder(id, req.Qty, req.Price)
	if msg != "" {
		c.Conflict(msg)
		return
	}
	c.Act("order.amend", "order:"+id,
		fmt.Sprintf("改单 qty=%d price=%.2f reason=%s", req.Qty, req.Price, req.Reason))
	c.OK(map[string]any{"order": o, "ok": true})
}

// hCancelOrder 撤单
func (s *Server) hCancelOrder(c *Ctx) {
	id := c.Path("id")
	if !s.State.CancelOrder(id) {
		c.Conflict("订单不存在或当前状态不可撤（仅 SENT/PARTIAL 可撤）")
		return
	}
	c.Act("order.cancel", "order:"+id, "撤单成功")
	c.OK(map[string]any{"ok": true, "order_id": id})
}

// hBatchCancel 批量撤单
func (s *Server) hBatchCancel(c *Ctx) {
	var req struct {
		OrderIDs []string `json:"order_ids"`
		Symbol   string   `json:"symbol"`
		Strategy string   `json:"strategy_id"`
	}
	if err := c.Bind(&req); err != nil {
		c.Bad("请求体格式错误：" + err.Error())
		return
	}
	targets := map[string]bool{}
	for _, id := range req.OrderIDs {
		targets[id] = true
	}
	if len(targets) == 0 {
		for _, o := range s.State.Book.OpenOrders() {
			if req.Symbol != "" && o.Symbol != req.Symbol {
				continue
			}
			if req.Strategy != "" && o.StrategyID != req.Strategy {
				continue
			}
			targets[o.OrderID] = true
		}
	}
	cancelled, failed := []string{}, []string{}
	for id := range targets {
		if s.State.CancelOrder(id) {
			cancelled = append(cancelled, id)
		} else {
			failed = append(failed, id)
		}
	}
	sort.Strings(cancelled)
	c.Act("order.batch_cancel", "orders", fmt.Sprintf("批量撤单 成功 %d 失败 %d", len(cancelled), len(failed)))
	c.OK(map[string]any{"cancelled": cancelled, "failed": failed, "count": len(cancelled)})
}

// ---------- 成交 / 信号 ----------

func (s *Server) hFills(c *Ctx) {
	p := ParsePage(c, 50)
	symbol := c.Query("symbol")
	all := s.State.Book.Fills()
	rows := make([]FillView, 0, len(all))
	for _, f := range all {
		if symbol != "" && f.Symbol != symbol {
			continue
		}
		if !Match(p.Filter, f.Symbol, f.FillID, f.OrderID) {
			continue
		}
		rows = append(rows, FillView{
			FillID: f.FillID, Symbol: f.Symbol, Side: f.Side.String(), Qty: f.FilledQty,
			Price: round2(f.FillPrice), Amount: round2(f.FillAmount),
			Time: time.UnixMilli(f.FillTime).Format("15:04:05"),
		})
	}
	page, meta := Slice(rows, p)
	c.OKPage(page, meta)
}

func (s *Server) hSignals(c *Ctx) {
	p := ParsePage(c, 50)
	strategy := c.Query("strategy")
	all := s.State.SignalList(0)
	rows := make([]SignalView, 0, len(all))
	for _, sg := range all {
		if strategy != "" && sg.Strategy != strategy {
			continue
		}
		if !Match(p.Filter, sg.Symbol, sg.Strategy) {
			continue
		}
		rows = append(rows, sg)
	}
	// 最新的在前
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
		rows[i], rows[j] = rows[j], rows[i]
	}
	page, meta := Slice(rows, p)
	c.OKPage(page, meta)
}

// ---------- 行情 ----------

func (s *Server) hMarketTick(c *Ctx) {
	all := s.State.MarketList()
	p := ParsePage(c, 200)
	rows := make([]MarketView, 0, len(all))
	for _, m := range all {
		if Match(p.Filter, m.Symbol) {
			rows = append(rows, m)
		}
	}
	page, meta := Slice(rows, p)
	c.OKPage(page, meta)
}

// Candle K 线
type Candle struct {
	T string  `json:"t"`
	O float64 `json:"o"`
	H float64 `json:"h"`
	L float64 `json:"l"`
	C float64 `json:"c"`
	V int64   `json:"v"`
}

// hKline K 线聚合：把逐笔按周期聚合成 OHLC
func (s *Server) hKline(c *Ctx) {
	symbol := c.Query("symbol")
	if symbol == "" {
		c.Bad("symbol 不能为空")
		return
	}
	period := strings.ToLower(c.Query("period"))
	if period == "" {
		period = "1m"
	}
	sec := map[string]int64{"1s": 1, "5s": 5, "30s": 30, "1m": 60, "5m": 300, "15m": 900}[period]
	if sec == 0 {
		c.Bad("period 仅支持 1s/5s/30s/1m/5m/15m")
		return
	}
	limit := c.I("limit", 120)
	if limit > 1000 {
		limit = 1000
	}
	ticks := s.State.HistoryOf(symbol)
	if len(ticks) == 0 {
		c.OK(map[string]any{"symbol": symbol, "period": period, "candles": []Candle{}})
		return
	}
	candles := aggregate(ticks, sec)
	if len(candles) > limit {
		candles = candles[len(candles)-limit:]
	}
	c.OK(map[string]any{"symbol": symbol, "period": period, "count": len(candles), "candles": candles})
}

func aggregate(ticks []common.Tick, periodSec int64) []Candle {
	periodMs := periodSec * 1000
	out := []Candle{}
	var cur *Candle
	var bucket int64 = -1
	for _, t := range ticks {
		b := t.Ts / periodMs
		if b != bucket {
			if cur != nil {
				out = append(out, *cur)
			}
			bucket = b
			cur = &Candle{
				T: time.UnixMilli(b * periodMs).Format("15:04:05"),
				O: t.Last, H: t.Last, L: t.Last, C: t.Last, V: t.Vol,
			}
			continue
		}
		if t.Last > cur.H {
			cur.H = t.Last
		}
		if t.Last < cur.L {
			cur.L = t.Last
		}
		cur.C = t.Last
		cur.V += t.Vol
	}
	if cur != nil {
		out = append(out, *cur)
	}
	return out
}

// DepthLevel 一档盘口
type DepthLevel struct {
	Price float64 `json:"price"`
	Vol   int64   `json:"vol"`
}

// hDepth 五档盘口
func (s *Server) hDepth(c *Ctx) {
	symbol := c.Query("symbol")
	if symbol == "" {
		c.Bad("symbol 不能为空")
		return
	}
	t, ok := s.State.TickOf(symbol)
	if !ok {
		c.NotFound("无该标的行情：" + symbol)
		return
	}
	asks, bids := []DepthLevel{}, []DepthLevel{}
	for i := 0; i < 5; i++ {
		asks = append(asks, DepthLevel{Price: round2(t.Ask[i]), Vol: int64(100 * (i + 1))})
		bids = append(bids, DepthLevel{Price: round2(t.Bid[i]), Vol: int64(100 * (i + 1))})
	}
	spread := 0.0
	if len(asks) > 0 && len(bids) > 0 {
		spread = round4(asks[0].Price - bids[0].Price)
	}
	c.OK(map[string]any{
		"symbol": symbol, "last": round2(t.Last), "ts": t.Ts,
		"asks": asks, "bids": bids, "spread": spread,
	})
}

func (s *Server) hPool(c *Ctx) { c.OK(s.State.Pool()) }

// ---------- 对账 ----------

func (s *Server) hReconcile(c *Ctx) { c.OK(s.State.Reconcile()) }

func (s *Server) hReconcileRun(c *Ctx) {
	r := s.State.Reconcile()
	c.Act("reconcile.run", "account:"+s.State.AccountID,
		fmt.Sprintf("手动对账：%v，差异 %d 处", r.OK, len(r.Diffs)))
	c.OK(r)
}
