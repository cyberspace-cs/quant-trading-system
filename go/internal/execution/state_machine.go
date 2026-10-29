package execution

import "quant/internal/common"

// Event 订单状态机事件
type Event int8

const (
	EvSend        Event = 0 // 报单
	EvPartialFill Event = 1 // 部分成交
	EvFullFill    Event = 2 // 全部成交
	EvCancel      Event = 3 // 撤单
	EvReject      Event = 4 // 废单/拒单
)

// Transition 状态跃迁校验：非法跃迁返回 false（保证状态机闭环、不丢单不重单）。
//
//	INIT --send--> SENT --partial--> PARTIAL --full--> FILLED
//	                   |                  |
//	                   +-----cancel-------+--> CANCELLED
//	                   +-----reject------->--> REJECTED
func Transition(o *common.Order, ev Event) bool {
	switch ev {
	case EvSend:
		if o.Status == common.Init {
			o.Status = common.Sent
			return true
		}
	case EvPartialFill:
		if o.Status == common.Sent || o.Status == common.Partial {
			o.Status = common.Partial
			return true
		}
	case EvFullFill:
		if o.Status == common.Sent || o.Status == common.Partial {
			o.Status = common.Filled
			return true
		}
	case EvCancel:
		if o.Status == common.Sent || o.Status == common.Partial {
			o.Status = common.Cancelled
			return true
		}
	case EvReject:
		if o.Status == common.Init || o.Status == common.Sent {
			o.Status = common.Rejected
			return true
		}
	}
	return false
}

// IsTerminal 终态判断
func IsTerminal(status int8) bool {
	return status == common.Filled || status == common.Cancelled || status == common.Rejected
}
