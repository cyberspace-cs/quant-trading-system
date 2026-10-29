package api

import (
	"fmt"
	"math"
	"math/rand"
	"sort"
	"time"
)

// ===================== 异步回测 =====================
//
// 回测是长任务（几十秒到几分钟），必须异步：POST 返回 job_id，
// 前端轮询 GET /backtests/{id} 看进度与结果。
// 生产实现会丢进任务队列（K8s Job / Celery / Argo），这里用 goroutine + 内存 job 表。

// BacktestJob 回测任务
type BacktestJob struct {
	ID         string             `json:"id"`
	Strategy   string             `json:"strategy"`
	Start      string             `json:"start"`
	End        string             `json:"end"`
	Params     map[string]float64 `json:"params,omitempty"`
	Status     string             `json:"status"` // queued/running/done/failed
	Progress   int                `json:"progress"`
	CreatedBy  string             `json:"created_by"`
	CreatedAt  int64              `json:"created_at"`
	FinishedAt int64              `json:"finished_at,omitempty"`
	DurationMs int64              `json:"duration_ms"`
	Result     *BacktestResult    `json:"result,omitempty"`
	Error      string             `json:"error,omitempty"`
}

// BacktestResult 回测结果
type BacktestResult struct {
	TotalReturn  float64       `json:"total_return"`
	AnnualReturn float64       `json:"annual_return"`
	Sharpe       float64       `json:"sharpe"`
	MaxDrawdown  float64       `json:"max_drawdown"`
	WinRate      float64       `json:"win_rate"`
	ProfitFactor float64       `json:"profit_factor"`
	Calmar       float64       `json:"calmar"`
	Turnover     float64       `json:"turnover"`
	Trades       int           `json:"trades"`
	Days         int           `json:"days"`
	Benchmark    float64       `json:"benchmark_return"`
	Excess       float64       `json:"excess_return"`
	Curve        []EquityPoint `json:"curve"`
	BenchCurve   []EquityPoint `json:"benchmark_curve"`
}

func (s *Server) startBacktest(strategy, start, end string, params map[string]float64, by string) *BacktestJob {
	if strategy == "" {
		strategy = "alpha_mf"
	}
	if start == "" {
		start = time.Now().AddDate(-1, 0, 0).Format("2006-01-02")
	}
	if end == "" {
		end = time.Now().Format("2006-01-02")
	}
	if params == nil {
		params = s.State.S.Params(strategy)
	}
	id := fmt.Sprintf("BT-%s-%03d", time.Now().Format("0102150405"), s.State.S.NextSeq())
	job := &BacktestJob{
		ID: id, Strategy: strategy, Start: start, End: end,
		Params: params, Status: "queued", CreatedBy: by, CreatedAt: time.Now().UnixMilli(),
	}
	s.State.S.mu.Lock()
	s.State.S.Backtest[id] = job
	s.State.S.mu.Unlock()

	go s.runBacktest(job)
	return job
}

// runBacktest 执行回测（确定性伪随机，保证同参数结果可复现）
func (s *Server) runBacktest(job *BacktestJob) {
	begin := time.Now()
	job.Status = "running"
	job.Progress = 5

	// 用 策略名+区间 派生种子：同参数重复回测结果一致（可复现性是回测的底线）
	seed := int64(0)
	for _, ch := range job.Strategy + job.Start + job.End {
		seed = seed*31 + int64(ch)
	}
	if seed < 0 {
		seed = -seed
	}
	rng := rand.New(rand.NewSource(seed))

	days := 250
	curve := make([]EquityPoint, 0, days+1)
	bench := make([]EquityPoint, 0, days+1)
	nav, bnav := 1.0, 1.0
	peak, maxDD := 1.0, 0.0
	wins, losses, winSum, lossSum := 0, 0, 0.0, 0.0
	trades := 0
	turnover := 0.0

	// 策略：alpha = 0.0006/日 + 市场 beta 0.8 + 特质收益，带轻微均值回归
	for i := 0; i <= days; i++ {
		if i%25 == 0 {
			job.Progress = 5 + i*90/days
			time.Sleep(12 * time.Millisecond) // 模拟计算耗时，让前端能看到进度
		}
		if i == 0 {
			curve = append(curve, EquityPoint{T: fmt.Sprintf("D%03d", i), V: 1.0})
			bench = append(bench, EquityPoint{T: fmt.Sprintf("D%03d", i), V: 1.0})
			continue
		}
		mkt := rng.NormFloat64() * 0.011
		alpha := 0.00055 + rng.NormFloat64()*0.0028
		r := 0.8*mkt + alpha
		br := mkt

		nav *= 1 + r
		bnav *= 1 + br
		curve = append(curve, EquityPoint{T: fmt.Sprintf("D%03d", i), V: round4(nav)})
		bench = append(bench, EquityPoint{T: fmt.Sprintf("D%03d", i), V: round4(bnav)})

		if nav > peak {
			peak = nav
		}
		if dd := (nav - peak) / peak; dd < maxDD {
			maxDD = dd
		}
		if r > 0 {
			wins++
			winSum += r
		} else {
			losses++
			lossSum += -r
		}
		trades += 2 + rng.Intn(4)
		turnover += 0.04 + rng.Float64()*0.03
	}

	rets := make([]float64, 0, days)
	for i := 1; i < len(curve); i++ {
		if curve[i-1].V > 0 {
			rets = append(rets, curve[i].V/curve[i-1].V-1)
		}
	}
	sharpe := sharpeOfCurve(rets)
	ann := math.Pow(nav, 252.0/float64(days)) - 1
	pf := 0.0
	if lossSum > 0 {
		pf = winSum / lossSum
	}
	calmar := 0.0
	if maxDD < 0 {
		calmar = ann / math.Abs(maxDD)
	}
	winRate := 0.0
	if wins+losses > 0 {
		winRate = float64(wins) / float64(wins+losses)
	}

	job.Result = &BacktestResult{
		TotalReturn: round4(nav - 1), AnnualReturn: round4(ann),
		Sharpe: round2(sharpe), MaxDrawdown: round4(maxDD),
		WinRate: round4(winRate), ProfitFactor: round2(pf), Calmar: round2(calmar),
		Turnover: round4(turnover / float64(days)), Trades: trades, Days: days,
		Benchmark: round4(bnav - 1), Excess: round4(nav - bnav),
		Curve: curve, BenchCurve: bench,
	}
	job.Progress = 100
	job.Status = "done"
	job.FinishedAt = time.Now().UnixMilli()
	job.DurationMs = time.Since(begin).Milliseconds()
}

func sharpeOfCurve(rets []float64) float64 {
	if len(rets) < 2 {
		return 0
	}
	var mean float64
	for _, r := range rets {
		mean += r
	}
	mean /= float64(len(rets))
	var v float64
	for _, r := range rets {
		v += (r - mean) * (r - mean)
	}
	std := math.Sqrt(v / float64(len(rets)))
	if std == 0 {
		return 0
	}
	return mean / std * math.Sqrt(252)
}

func (s *Server) hBacktests(c *Ctx) {
	p := ParsePage(c, 20)
	s.State.S.mu.RLock()
	all := make([]*BacktestJob, 0, len(s.State.S.Backtest))
	for _, j := range s.State.S.Backtest {
		cp := *j
		cp.Result = nil // 列表不返回曲线，省带宽
		all = append(all, &cp)
	}
	s.State.S.mu.RUnlock()
	sort.Slice(all, func(i, j int) bool { return all[i].CreatedAt > all[j].CreatedAt })
	page, meta := Slice(all, p)
	c.OKPage(page, meta)
}

func (s *Server) hBacktestDetail(c *Ctx) {
	id := c.Path("id")
	s.State.S.mu.RLock()
	job, ok := s.State.S.Backtest[id]
	var cp BacktestJob
	if ok {
		cp = *job
	}
	s.State.S.mu.RUnlock()
	if !ok {
		c.NotFound("回测任务不存在：" + id)
		return
	}
	c.OK(&cp)
}

func (s *Server) hCreateBacktest(c *Ctx) {
	var req struct {
		Strategy string             `json:"strategy"`
		Start    string             `json:"start"`
		End      string             `json:"end"`
		Params   map[string]float64 `json:"params"`
	}
	if err := c.Bind(&req); err != nil {
		c.Bad("请求体格式错误：" + err.Error())
		return
	}
	if req.Strategy != "" && !s.strategyExists(req.Strategy) {
		c.NotFound("策略不存在：" + req.Strategy)
		return
	}
	job := s.startBacktest(req.Strategy, req.Start, req.End, req.Params, c.User.Name)
	c.Act("backtest.create", "backtest:"+job.ID,
		fmt.Sprintf("策略 %s 回测 %s ~ %s", job.Strategy, job.Start, job.End))
	c.OK(job)
}
