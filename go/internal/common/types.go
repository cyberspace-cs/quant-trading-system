// Package common 定义量化交易系统的领域模型与基础设施接口。
// 设计目标：所有强依赖（Redis / Kafka / 柜台 / MySQL）都以接口形式抽象，
// 生产环境替换为真实实现（go-redis / sarama / FIX / database/sql），
// 本仓库提供内存实现，保证 go run 端到端可跑通，用于面试演示与回放。
package common

import "time"

// ===================== 枚举 =====================

// Side 买卖方向
type Side int8

const (
	Buy  Side = 1
	Sell Side = -1
)

func (s Side) String() string {
	if s == Buy {
		return "BUY"
	}
	return "SELL"
}

// OrderStatus 订单状态机枚举（见 execution/state_machine.go）
const (
	Init      int8 = 0 // 初始化
	Sent      int8 = 1 // 已报
	Partial   int8 = 2 // 部分成交
	Filled    int8 = 3 // 已成
	Cancelled int8 = 4 // 已撤
	Rejected  int8 = 5 // 废单/被拒
)

func StatusName(s int8) string {
	switch s {
	case Init:
		return "INIT"
	case Sent:
		return "SENT"
	case Partial:
		return "PARTIAL"
	case Filled:
		return "FILLED"
	case Cancelled:
		return ("CANCELLED")
	case Rejected:
		return "REJECTED"
	}
	return "UNKNOWN"
}

// ===================== 领域模型 =====================

// Tick 实时行情快照（盘口 + 逐笔）
type Tick struct {
	Symbol string
	Last   float64
	Ask    [5]float64
	Bid    [5]float64
	Vol    int64
	Ts     int64
}

// Factor 实时因子值
type Factor struct {
	Symbol string
	Name   string
	Value  float64
	Ts     int64
}

// Signal 策略生成的调仓信号（标准化委托指令）
type Signal struct {
	StrategyID string
	Symbol     string
	Side       Side
	Qty        int64
	Price      float64 // 0 表示市价
	Ts         int64
}

// Order 订单（不可变流水在 MySQL；内存/Redis 维护实时状态）
type Order struct {
	OrderID    string
	AccountID  string
	StrategyID string
	Symbol     string
	Side       Side
	OrderType  int8 // 1 限价 2 市价
	OrigQty    int64
	FilledQty  int64
	Price      float64
	Status     int8
	RejectRsn  string
	ParentID   string // 母单（拆单）
	CreatedAt  int64
	UpdatedAt  int64
}

// RemainQty 剩余未成交量
func (o *Order) RemainQty() int64 { return o.OrigQty - o.FilledQty }

// Fill 逐笔成交回执（柜台为唯一真值）
type Fill struct {
	FillID     string
	OrderID    string
	Symbol     string
	Side       Side
	FilledQty  int64
	FillPrice  float64
	FillAmount float64
	FillTime   int64
}

// Position 实时持仓
type Position struct {
	Symbol      string
	Qty         int64
	FrozenQty   int64
	AvgCost     float64
	MarketValue float64
	PnL         float64
}

// Capital 资金账户
type Capital struct {
	AccountID string
	Available float64
	Frozen    float64
	Total     float64
	Nav       float64
}

// RiskRule 风控规则
type RiskRule struct {
	RuleID    string
	Dimension string // position/industry/pnl/drawdown/limit/leveragelot
	Threshold float64
	Action    string // REJECT/WARN/HALT
}

// ===================== 基础设施接口（生产替换为真实实现）=====================

// Cache Redis 语义缓存接口（生产 = go-redis Cluster）
type Cache interface {
	HSet(key, field string, val string)
	HGetAll(key string) map[string]string
	Set(key, val string)
	Get(key string) (string, bool)
	Incr(key string) int64
	GetBit(key string, offset int) int
	SetBit(key string, offset, val int)
	SAdd(key string, members ...string)
	SIsMember(key string, member string) bool
	Del(key string)
}

// EventBus Kafka 语义事件总线（生产 = sarama，分区按 symbol 哈希）
type EventBus interface {
	Publish(topic string, payload []byte)
	Subscribe(topic string, handler func(payload []byte))
}

// Broker 券商柜台接口（生产 = FIX / 原生 API）
type Broker interface {
	// Place 上报母单，返回模拟成交（真实环境为异步回执 + 状态推送）
	Place(o *Order) ([]Fill, error)
}

// Ledger 资产台账接口（生产 = database/sql + MySQL/TiDB）
type Ledger interface {
	SaveOrder(o *Order)
	SaveFill(f *Fill)
	ApplyFill(f *Fill) // 权威持仓/资金记账（以柜台回执为真值）
	UpsertPosition(p *Position)
	SaveRiskLog(strategyID, ruleID, dimension, action, reason string)
	Snapshot() string
}

// ===================== 工具 =====================

// NowMs 当前毫秒时间戳
func NowMs() int64 { return time.Now().UnixMilli() }
