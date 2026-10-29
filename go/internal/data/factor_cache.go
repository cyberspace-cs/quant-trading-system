package data

import (
	"fmt"

	"quant/internal/common"
)

// 键前缀（与 docs/02-redis-design.md 对齐）
const (
	KeyTick      = "quant:tick:"          // Hash
	KeyFactor    = "quant:factor:"        // String: quant:factor:{code}:{name}
	KeyPoolStock = "quant:pool:stock"     // Set
	KeyLimitUp   = "quant:limit:up"       // Bitmap
	KeyLimitDown = "quant:limit:down"     // Bitmap
	KeyBlacklist = "quant:pool:blacklist" // Set
	KeyStatus    = "quant:status:trading" // String
)

// FactorCache 将行情与实时因子写入 Redis（生产由 Flink 算子完成）。
type FactorCache struct {
	cache common.Cache
}

func NewFactorCache(c common.Cache) *FactorCache { return &FactorCache{cache: c} }

// WriteTick 写入最新盘口（Hash，毫秒级覆盖）
func (fc *FactorCache) WriteTick(t common.Tick) {
	key := KeyTick + t.Symbol
	fc.cache.HSet(key, "last", fmt.Sprintf("%.2f", t.Last))
	fc.cache.HSet(key, "vol", fmt.Sprintf("%d", t.Vol))
	fc.cache.HSet(key, "ts", fmt.Sprintf("%d", t.Ts))
	for i := 0; i < 5; i++ {
		fc.cache.HSet(key, fmt.Sprintf("ask%d", i+1), fmt.Sprintf("%.2f", t.Ask[i]))
		fc.cache.HSet(key, fmt.Sprintf("bid%d", i+1), fmt.Sprintf("%.2f", t.Bid[i]))
	}
}

// WriteFactor 写入单股票实时因子
func (fc *FactorCache) WriteFactor(f common.Factor) {
	fc.cache.Set(KeyFactor+f.Symbol+":"+f.Name, fmt.Sprintf("%.6f", f.Value))
}

// ReadFactor 读取因子（策略层调用）
func (fc *FactorCache) ReadFactor(symbol, name string) (float64, bool) {
	v, ok := fc.cache.Get(KeyFactor + symbol + ":" + name)
	if !ok {
		return 0, false
	}
	var f float64
	fmt.Sscanf(v, "%f", &f)
	return f, true
}

// InitPool 盘前初始化股票池 / 黑名单 / 涨跌停位图
func (fc *FactorCache) InitPool(pool []string, blacklist []string, intIDs map[string]int) {
	fc.cache.SAdd(KeyPoolStock, pool...)
	if len(blacklist) > 0 {
		fc.cache.SAdd(KeyBlacklist, blacklist...)
	}
	for _, id := range intIDs {
		fc.cache.SetBit(KeyLimitUp, id, 0)
		fc.cache.SetBit(KeyLimitDown, id, 0)
	}
	fc.cache.Set(KeyStatus, "open")
}

// MarkLimit 标记涨跌停（供执行/风控快速过滤）
func (fc *FactorCache) MarkLimit(intID int, up bool) {
	if up {
		fc.cache.SetBit(KeyLimitUp, intID, 1)
	} else {
		fc.cache.SetBit(KeyLimitDown, intID, 1)
	}
}
