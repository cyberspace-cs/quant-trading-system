package api

import (
	"fmt"
	"hash/fnv"
	"math"
	"time"
)

// ===================== 因子库接口 =====================

// hFactors 兼容旧行为：带 symbol 返回该标的实时因子值；否则返回因子清单
func (s *Server) hFactors(c *Ctx) {
	if sym := c.Query("symbol"); sym != "" {
		c.OK(s.State.FactorOf(sym))
		return
	}
	s.hFactorMeta(c)
}

func (s *Server) hFactorMeta(c *Ctx) {
	p := ParsePage(c, 200)
	category := c.Query("category")
	status := c.Query("status")
	all := s.State.S.FactorList()
	rows := make([]*FactorMeta, 0, len(all))
	for _, f := range all {
		if category != "" && f.Category != category {
			continue
		}
		if status != "" && f.Status != status {
			continue
		}
		if !Match(p.Filter, f.ID, f.Name, f.Category, f.Description) {
			continue
		}
		rows = append(rows, f)
	}
	// 按 IC 绝对值排序：IC 高的因子优先关注
	if p.Sort == "ic" || p.Sort == "" {
		for i := 0; i < len(rows); i++ {
			for j := i + 1; j < len(rows); j++ {
				if math.Abs(rows[j].IC) > math.Abs(rows[i].IC) {
					rows[i], rows[j] = rows[j], rows[i]
				}
			}
		}
	}
	page, meta := Slice(rows, p)
	// 分类汇总
	byCat := map[string]int{}
	for _, f := range all {
		byCat[f.Category]++
	}
	c.OKPage(map[string]any{"factors": page, "by_category": byCat}, meta)
}

func (s *Server) hCreateFactor(c *Ctx) {
	var req struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Category    string `json:"category"`
		Formula     string `json:"formula"`
		Direction   int    `json:"direction"`
		Description string `json:"description"`
	}
	if err := c.Bind(&req); err != nil {
		c.Bad("请求体格式错误：" + err.Error())
		return
	}
	if req.ID == "" || req.Name == "" || req.Formula == "" {
		c.Bad("id / name / formula 必填")
		return
	}
	if len(req.ID) > 32 {
		c.Bad("因子 ID 不能超过 32 字符")
		return
	}
	for _, ch := range req.ID {
		ok := (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') || ch == '_'
		if !ok {
			c.Bad("因子 ID 只允许小写字母、数字、下划线")
			return
		}
	}
	if s.State.S.Factor(req.ID) != nil {
		c.Conflict("因子已存在：" + req.ID)
		return
	}
	now := time.Now().UnixMilli()
	f := &FactorMeta{
		ID: req.ID, Name: req.Name, Category: def(req.Category, "自定义"),
		Formula: req.Formula, Direction: defInt(req.Direction, 1),
		Owner: c.User.Name, Status: "testing", Description: req.Description,
		Coverage: 0.95, CreatedAt: now, UpdatedAt: now,
	}
	s.State.S.PutFactor(f)
	c.Act("factor.create", "factor:"+req.ID, "注册因子 "+req.Name+"："+req.Formula)
	c.OK(f)
}

func (s *Server) hUpdateFactor(c *Ctx) {
	id := c.Path("id")
	f := s.State.S.Factor(id)
	if f == nil {
		c.NotFound("因子不存在：" + id)
		return
	}
	var req struct {
		Name        *string `json:"name"`
		Category    *string `json:"category"`
		Formula     *string `json:"formula"`
		Status      *string `json:"status"`
		Direction   *int    `json:"direction"`
		Description *string `json:"description"`
	}
	if err := c.Bind(&req); err != nil {
		c.Bad("请求体格式错误：" + err.Error())
		return
	}
	if req.Status != nil {
		switch *req.Status {
		case "online", "offline", "testing":
		default:
			c.Bad("status 仅支持 online / offline / testing")
			return
		}
		f.Status = *req.Status
	}
	if req.Name != nil {
		f.Name = *req.Name
	}
	if req.Category != nil {
		f.Category = *req.Category
	}
	if req.Formula != nil {
		f.Formula = *req.Formula
	}
	if req.Direction != nil {
		f.Direction = *req.Direction
	}
	if req.Description != nil {
		f.Description = *req.Description
	}
	f.UpdatedAt = time.Now().UnixMilli()
	s.State.S.PutFactor(f)
	c.Act("factor.update", "factor:"+id, "更新因子 "+id)
	c.OK(f)
}

func (s *Server) hDeleteFactor(c *Ctx) {
	id := c.Path("id")
	f := s.State.S.Factor(id)
	if f == nil {
		c.NotFound("因子不存在：" + id)
		return
	}
	if f.Builtin {
		c.Conflict("内置因子不允许删除（只能下线 status=offline），防止策略静默失效")
		return
	}
	s.State.S.DelFactor(id)
	c.Act("factor.delete", "factor:"+id, "删除自定义因子 "+f.Name)
	c.OK(map[string]any{"ok": true, "id": id})
}

// ICResult 因子 IC 评价
type ICResult struct {
	FactorID   string    `json:"factor_id"`
	Window     string    `json:"window"`
	ICMean     float64   `json:"ic_mean"`
	ICStd      float64   `json:"ic_std"`
	ICIR       float64   `json:"icir"`
	TStat      float64   `json:"tstat"`
	ICWinRate  float64   `json:"ic_win_rate"`
	Series     []float64 `json:"series"`
	Periods    []string  `json:"periods"`
	Deciles    []float64 `json:"deciles"`    // 10 分层年化收益
	LongShort  float64   `json:"long_short"` // 多空年化
	Turnover   float64   `json:"turnover"`
	Coverage   float64   `json:"coverage"`
	Conclusion string    `json:"conclusion"`
}

// hFactorIC 因子 IC 序列与分层收益
//
// 生产实现：从 factor_value 表按日取因子值，与 T+1~T+5 收益做秩相关（RankIC），
// 再按 IC 序列算 ICIR = mean(IC)/std(IC)、t = ICIR*sqrt(n)、10 分层组合收益。
// 演示环境没有落库的历史因子值，用「由因子 ID 派生的确定性序列」模拟同一套计算口径，
// 保证每次调用结果稳定、可复现（面试可以对照解释每一步）。
func (s *Server) hFactorIC(c *Ctx) {
	id := c.Path("id")
	f := s.State.S.Factor(id)
	if f == nil {
		c.NotFound("因子不存在：" + id)
		return
	}
	n := c.I("window", 24)
	if n < 6 {
		n = 6
	}
	if n > 120 {
		n = 120
	}
	series := make([]float64, n)
	periods := make([]string, n)
	sum, sumSq, wins := 0.0, 0.0, 0
	nowT := time.Now()
	for i := 0; i < n; i++ {
		// 以 IC 均值 + 由 (id,i) 派生的噪声构造序列，末期向目标 IC 收敛
		noise := pseudoNoise(id, i)*0.03 - 0.015
		v := f.IC + noise
		series[i] = round4(v)
		periods[i] = nowT.AddDate(0, -(n - i), 0).Format("2006-01")
		sum += v
		sumSq += v * v
		if v > 0 {
			wins++
		}
	}
	mean := sum / float64(n)
	std := math.Sqrt(math.Max(sumSq/float64(n)-mean*mean, 0))
	icir, tstat := 0.0, 0.0
	if std > 0 {
		icir = mean / std
		tstat = icir * math.Sqrt(float64(n))
	}
	// 10 分层：按 IC 强度放大，方向由 factor.Direction 决定
	deciles := make([]float64, 10)
	for i := 0; i < 10; i++ {
		base := mean * 12 * float64(f.Direction) * (float64(i) - 4.5) / 4.5
		deciles[i] = round4(base)
	}
	ls := round4((deciles[9] - deciles[0]))

	conclusion := "IC 不显著，建议观察"
	switch {
	case math.Abs(icir) >= 0.5 && math.Abs(tstat) >= 2:
		conclusion = "IC 显著且稳定，可进入组合"
	case math.Abs(tstat) >= 2:
		conclusion = "IC 显著但波动大，需降低权重"
	}

	c.OK(&ICResult{
		FactorID: id, Window: fmt.Sprintf("%dM", n),
		ICMean: round4(mean), ICStd: round4(std), ICIR: round4(icir), TStat: round2(tstat),
		ICWinRate: round4(float64(wins) / float64(n)),
		Series:    series, Periods: periods, Deciles: deciles, LongShort: ls,
		Turnover: f.Turnover, Coverage: f.Coverage, Conclusion: conclusion,
	})
}

// pseudoNoise 确定性伪随机 [-1,1]
func pseudoNoise(seed string, i int) float64 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(fmt.Sprintf("%s#%d", seed, i)))
	v := float64(h.Sum32()%10000) / 10000.0
	return v*2 - 1
}

func def(v, d string) string {
	if v == "" {
		return d
	}
	return v
}

func defInt(v, d int) int {
	if v == 0 {
		return d
	}
	return v
}
