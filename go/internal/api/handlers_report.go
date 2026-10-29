package api

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"quant/internal/common"
)

// ===================== 报表：日报 / 收益归因 =====================

// DailyReport 盘后日报
type DailyReport struct {
	Date          string         `json:"date"`
	AccountID     string         `json:"account_id"`
	Nav           float64        `json:"nav"`
	TotalAssets   float64        `json:"total_assets"`
	DayPnL        float64        `json:"day_pnl"`
	DayPnLPct     float64        `json:"day_pnl_pct"`
	RealizedPnL   float64        `json:"realized_pnl"`
	UnrealizedPnL float64        `json:"unrealized_pnl"`
	Orders        int            `json:"orders"`
	Fills         int            `json:"fills"`
	Turnover      float64        `json:"turnover"`
	RiskRejects   int            `json:"risk_rejects"`
	MaxDrawdown   float64        `json:"max_drawdown"`
	Sharpe        float64        `json:"sharpe"`
	PositionCnt   int            `json:"position_count"`
	TopGainers    []SymbolPnL    `json:"top_gainers"`
	TopLosers     []SymbolPnL    `json:"top_losers"`
	RiskByRule    map[string]int `json:"risk_by_rule"`
	GeneratedAt   string         `json:"generated_at"`
}

// SymbolPnL 单标的盈亏
type SymbolPnL struct {
	Symbol   string  `json:"symbol"`
	Qty      int64   `json:"qty"`
	PnL      float64 `json:"pnl"`
	PnLPct   float64 `json:"pnl_pct"`
	Industry string  `json:"industry"`
}

func (s *Server) hDailyReport(c *Ctx) {
	date := c.Query("date")
	if date == "" {
		date = s.State.TradeDate
	}
	ov := s.State.Overview()
	perf := s.State.Performance()
	pos := s.State.Positions()

	unreal := 0.0
	for _, p := range pos {
		unreal += p.PnL
	}
	rep := &DailyReport{
		Date: date, AccountID: ov.AccountID, Nav: ov.Nav, TotalAssets: ov.Total,
		DayPnL: ov.DayPnL, DayPnLPct: ov.DayPnLPct,
		RealizedPnL: round2(s.State.Book.RealizedPnL()), UnrealizedPnL: round2(unreal),
		Orders: ov.Orders, Fills: ov.Fills, Turnover: ov.Turnover,
		RiskRejects: ov.RiskRejects, MaxDrawdown: perf.MaxDrawdown, Sharpe: perf.Sharpe,
		PositionCnt: len(pos), TopGainers: []SymbolPnL{}, TopLosers: []SymbolPnL{},
		RiskByRule: map[string]int{}, GeneratedAt: time.Now().Format("2006-01-02 15:04:05"),
	}
	rows := make([]SymbolPnL, 0, len(pos))
	for _, x := range pos {
		rows = append(rows, SymbolPnL{
			Symbol: x.Symbol, Qty: x.Qty, PnL: x.PnL, PnLPct: x.PnLPct, Industry: x.Industry,
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].PnL > rows[j].PnL })
	for i := 0; i < len(rows) && i < 5; i++ {
		rep.TopGainers = append(rep.TopGainers, rows[i])
	}
	for i := len(rows) - 1; i >= 0 && len(rep.TopLosers) < 5; i-- {
		if rows[i].PnL < 0 {
			rep.TopLosers = append(rep.TopLosers, rows[i])
		}
	}
	for _, l := range s.State.Book.RiskLogs() {
		rep.RiskByRule[l.RuleID]++
	}

	if c.Query("format") == "csv" {
		s.writeCSV(c, "daily_report_"+date+".csv",
			[]string{"item", "value"},
			[][]string{
				{"date", rep.Date}, {"account", rep.AccountID},
				{"nav", fmt.Sprintf("%.4f", rep.Nav)},
				{"total_assets", fmt.Sprintf("%.2f", rep.TotalAssets)},
				{"day_pnl", fmt.Sprintf("%.2f", rep.DayPnL)},
				{"day_pnl_pct", fmt.Sprintf("%.4f", rep.DayPnLPct)},
				{"realized_pnl", fmt.Sprintf("%.2f", rep.RealizedPnL)},
				{"unrealized_pnl", fmt.Sprintf("%.2f", rep.UnrealizedPnL)},
				{"orders", fmt.Sprintf("%d", rep.Orders)}, {"fills", fmt.Sprintf("%d", rep.Fills)},
				{"turnover", fmt.Sprintf("%.4f", rep.Turnover)},
				{"risk_rejects", fmt.Sprintf("%d", rep.RiskRejects)},
				{"max_drawdown", fmt.Sprintf("%.4f", rep.MaxDrawdown)},
				{"sharpe", fmt.Sprintf("%.2f", rep.Sharpe)},
			})
		return
	}
	c.OK(rep)
}

// AttrRow 归因行
type AttrRow struct {
	Key          string  `json:"key"`
	PnL          float64 `json:"pnl"`
	Contribution float64 `json:"contribution"` // 占总盈亏比例
	Orders       int     `json:"orders"`
	Fills        int     `json:"fills"`
	Turnover     float64 `json:"turnover"`
}

// Attribution 收益归因结果
type Attribution struct {
	Dim        string    `json:"dim"`
	TotalPnL   float64   `json:"total_pnl"`
	Rows       []AttrRow `json:"rows"`
	ByStrategy []AttrRow `json:"by_strategy"`
	ByIndustry []AttrRow `json:"by_industry"`
	BySymbol   []AttrRow `json:"by_symbol"`
	Generated  string    `json:"generated_at"`
}

// hAttribution 收益归因：按策略 / 行业 / 标的拆解
//
// 口径（与私募归因报告一致）：
//
//	单标的 PnL = 卖出金额 - 买入金额 + 期末持仓市值（mark-to-market 现金流法）
//	策略归因 = 该策略所有订单对应成交的现金流
//	行业归因 = 行业下所有标的 PnL 汇总
func (s *Server) hAttribution(c *Ctx) {
	strategyOf := map[string]string{} // orderID -> strategy
	orderCnt := map[string]int{}
	for _, o := range s.State.Book.Orders() {
		strategyOf[o.OrderID] = def(o.StrategyID, "manual")
		orderCnt[o.StrategyID]++
	}
	type agg struct {
		pnl, turnover float64
		fills         int
	}
	symAgg := map[string]*agg{}
	stratAgg := map[string]*agg{}
	for _, f := range s.State.Book.Fills() {
		sym := f.Symbol
		strat := def(strategyOf[f.OrderID], "manual")
		if strat == "manual" {
			// 子单 OrderID 形如 XXX-C01，回退到母单
			if i := strings.LastIndex(f.OrderID, "-C"); i > 0 {
				strat = def(strategyOf[f.OrderID[:i]], "manual")
			}
		}
		if symAgg[sym] == nil {
			symAgg[sym] = &agg{}
		}
		if stratAgg[strat] == nil {
			stratAgg[strat] = &agg{}
		}
		amt := f.FillAmount
		sign := -1.0
		if f.Side == common.Sell {
			sign = 1.0
		}
		symAgg[sym].pnl += sign * amt
		symAgg[sym].turnover += amt
		symAgg[sym].fills++
		stratAgg[strat].pnl += sign * amt
		stratAgg[strat].turnover += amt
		stratAgg[strat].fills++
	}
	// 期末持仓按最新价计入市值
	for sym, p := range s.State.Book.Positions() {
		if p.Qty == 0 {
			continue
		}
		px := s.State.PriceOf(sym)
		if px == 0 {
			px = p.AvgCost
		}
		if symAgg[sym] == nil {
			symAgg[sym] = &agg{}
		}
		symAgg[sym].pnl += float64(p.Qty) * px
	}

	totalPnL := 0.0
	for _, a := range symAgg {
		totalPnL += a.pnl
	}
	build := func(m map[string]*agg, order map[string]int) []AttrRow {
		out := make([]AttrRow, 0, len(m))
		for k, a := range m {
			contrib := 0.0
			if totalPnL != 0 {
				contrib = a.pnl / totalPnL
			}
			out = append(out, AttrRow{
				Key: k, PnL: round2(a.pnl), Contribution: round4(contrib),
				Orders: order[k], Fills: a.fills, Turnover: round2(a.turnover),
			})
		}
		sort.Slice(out, func(i, j int) bool { return out[i].PnL > out[j].PnL })
		return out
	}
	// 行业聚合
	indAgg := map[string]*agg{}
	for sym, a := range symAgg {
		ind := def(s.State.Industry[sym], "其他")
		if indAgg[ind] == nil {
			indAgg[ind] = &agg{}
		}
		indAgg[ind].pnl += a.pnl
		indAgg[ind].turnover += a.turnover
		indAgg[ind].fills += a.fills
	}

	res := &Attribution{
		Dim: def(c.Query("dim"), "all"), TotalPnL: round2(totalPnL),
		ByStrategy: build(stratAgg, orderCnt),
		ByIndustry: build(indAgg, nil),
		BySymbol:   build(symAgg, nil),
		Generated:  time.Now().Format("2006-01-02 15:04:05"),
	}
	switch res.Dim {
	case "strategy":
		res.Rows = res.ByStrategy
	case "industry":
		res.Rows = res.ByIndustry
	case "symbol":
		res.Rows = res.BySymbol
	default:
		res.Rows = res.BySymbol
	}

	if c.Query("format") == "csv" {
		rows := [][]string{}
		for _, r := range res.Rows {
			rows = append(rows, []string{r.Key, fmt.Sprintf("%.2f", r.PnL), fmt.Sprintf("%.4f", r.Contribution), fmt.Sprintf("%d", r.Fills)})
		}
		s.writeCSV(c, "attribution_"+res.Dim+".csv", []string{"key", "pnl", "contribution", "fills"}, rows)
		return
	}
	c.OK(res)
}

// writeCSV 统一 CSV 导出（Excel 中文需 BOM，否则乱码）
func (s *Server) writeCSV(c *Ctx, filename string, header []string, rows [][]string) {
	var b strings.Builder
	b.WriteString("\xEF\xBB\xBF")
	b.WriteString(strings.Join(header, ","))
	b.WriteString("\n")
	for _, r := range rows {
		b.WriteString(strings.Join(r, ","))
		b.WriteString("\n")
	}
	c.W.Header().Set("Content-Type", "text/csv; charset=utf-8")
	c.W.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	c.W.WriteHeader(200)
	_, _ = c.W.Write([]byte(b.String()))
}
