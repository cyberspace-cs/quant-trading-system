package execution

import (
	"testing"

	"quant/internal/common"
)

// TestTransition 订单状态机：合法跃迁与终态不可逆
func TestTransition(t *testing.T) {
	o := &common.Order{Status: common.Init}
	if !Transition(o, EvSend) || o.Status != common.Sent {
		t.Fatal("INIT -> SENT 失败")
	}
	if !Transition(o, EvPartialFill) || o.Status != common.Partial {
		t.Fatal("SENT -> PARTIAL 失败")
	}
	if !Transition(o, EvFullFill) || o.Status != common.Filled {
		t.Fatal("PARTIAL -> FILLED 失败")
	}
	if Transition(o, EvCancel) {
		t.Fatal("终态 FILLED 不应允许撤单")
	}
	if Transition(o, EvSend) {
		t.Fatal("终态不应再次报单")
	}
}

// TestSliceSum 拆单：子单量之和必须等于母单量，且 ParentID 正确
func TestSliceSum(t *testing.T) {
	parent := common.Order{OrderID: "P1", OrigQty: 100000, Side: common.Buy, Price: 10}
	for _, algo := range []Algo{TWAP, VWAP, Adaptive} {
		children := Slice(parent, algo, 3, 1000, 0)
		var sum int64
		for _, c := range children {
			sum += c.OrigQty
			if c.ParentID != "P1" {
				t.Fatalf("%v 子单 ParentID 错误", algo)
			}
			if c.OrigQty%100 != 0 && c != children[len(children)-1] {
				t.Fatalf("%v 非整手拆单", algo)
			}
		}
		if sum != parent.OrigQty {
			t.Fatalf("%v 拆单总量 %d != 母单 %d", algo, sum, parent.OrigQty)
		}
	}
}
