package execution

import (
	"encoding/json"
	"fmt"
	"strconv"

	"quant/internal/common"
)

// OMS 订单管理系统：订单状态机 + 柜台对接 + 实时缓存 + 台账落地。
type OMS struct {
	cache  common.Cache
	bus    common.EventBus
	broker common.Broker
	ledger common.Ledger
}

func NewOMS(cache common.Cache, bus common.EventBus, broker common.Broker, ledger common.Ledger) *OMS {
	return &OMS{cache: cache, bus: bus, broker: broker, ledger: ledger}
}

// Submit 上报母单：INIT -> SENT -> 柜台成交 -> PARTIAL/FILLED，全程更新缓存与台账。
func (o *OMS) Submit(order *common.Order) []common.Fill {
	if !Transition(order, EvSend) {
		return nil
	}
	order.UpdatedAt = common.NowMs()
	o.syncOrderCache(order)

	fills, err := o.broker.Place(order)
	if err != nil {
		Transition(order, EvReject)
		order.RejectRsn = err.Error()
		order.UpdatedAt = common.NowMs()
		o.syncOrderCache(order)
		o.ledger.SaveOrder(order)
		return nil
	}

	var done []common.Fill
	for i := range fills {
		// 必须取副本再传指针：Go 1.21 的 range 变量在整个循环中被复用，
		// 直接 &f 会让人台账里所有成交都指向同一块内存，最终全部变成最后一笔。
		f := fills[i]
		order.FilledQty += f.FilledQty
		if order.FilledQty >= order.OrigQty {
			Transition(order, EvFullFill)
		} else {
			Transition(order, EvPartialFill)
		}
		order.UpdatedAt = f.FillTime

		o.ledger.SaveFill(&f)
		o.ledger.ApplyFill(&f)
		o.syncPositionCache(f)
		o.syncOrderCache(order)

		if payload, err := json.Marshal(f); err == nil {
			o.bus.Publish("order.fill", payload)
		}
		done = append(done, f)
	}
	o.ledger.SaveOrder(order)
	return done
}

// Cancel 撤单：仅 SENT/PARTIAL 可撤，撤单后同步缓存。
func (o *OMS) Cancel(order *common.Order) bool {
	if !Transition(order, EvCancel) {
		return false
	}
	order.UpdatedAt = common.NowMs()
	o.syncOrderCache(order)
	o.ledger.SaveOrder(order)
	return true
}

// syncOrderCache 写 quant:order:live:{order_id}
func (o *OMS) syncOrderCache(order *common.Order) {
	key := "quant:order:live:" + order.OrderID
	o.cache.HSet(key, "status", common.StatusName(order.Status))
	o.cache.HSet(key, "filled_qty", strconv.FormatInt(order.FilledQty, 10))
	o.cache.HSet(key, "remain_qty", strconv.FormatInt(order.RemainQty(), 10))
	o.cache.HSet(key, "price", fmt.Sprintf("%.2f", order.Price))
	o.cache.HSet(key, "symbol", order.Symbol)
}

// syncPositionCache 实时持仓回写 quant:position:real:{code}（展示口径，盘后由台账校准）
func (o *OMS) syncPositionCache(f common.Fill) {
	key := "quant:position:real:" + f.Symbol
	h := o.cache.HGetAll(key)
	qty, _ := strconv.ParseInt(h["qty"], 10, 64)
	avg, _ := strconv.ParseFloat(h["avg_cost"], 64)

	if f.Side == common.Buy {
		newQty := qty + f.FilledQty
		if newQty > 0 {
			avg = (avg*float64(qty) + f.FillPrice*float64(f.FilledQty)) / float64(newQty)
		}
		qty = newQty
	} else {
		qty -= f.FilledQty
		if qty <= 0 {
			qty = 0
			avg = 0
		}
	}
	o.cache.HSet(key, "qty", strconv.FormatInt(qty, 10))
	o.cache.HSet(key, "avg_cost", fmt.Sprintf("%.4f", avg))
}
