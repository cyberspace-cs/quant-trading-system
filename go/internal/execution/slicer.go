// Package execution 交易执行层：智能拆单 + 订单状态机 + OMS。
// 解决冲击成本、滑点、拆单隐蔽性；大单切碎、间隔下发、避免被对手盘捕捉。
package execution

import (
	"fmt"

	"quant/internal/common"
)

// Algo 拆单算法
type Algo int8

const (
	TWAP     Algo = 0 // 时间切片
	VWAP     Algo = 1 // 量加权
	Adaptive Algo = 2 // 自适应（按盘口深度动态）
)

func (a Algo) String() string {
	switch a {
	case TWAP:
		return "TWAP"
	case VWAP:
		return "VWAP"
	case Adaptive:
		return "ADAPTIVE"
	}
	return "UNKNOWN"
}

// Slice 将母单拆为子单。返回子单列表（ParentID 指向母单，数量之和 == 母单量）。
func Slice(parent common.Order, algo Algo, slices int, intervalMs int64, now int64) []common.Order {
	if slices < 1 {
		slices = 1
	}
	weights := weightsFor(algo, slices)
	totalQty := parent.OrigQty

	// 按权重分配数量，最后一个补足差额，保证总和精确
	var children []common.Order
	allocated := int64(0)
	for i := 0; i < slices; i++ {
		q := int64(float64(totalQty) * weights[i])
		q = q / 100 * 100 // 整手
		if i == slices-1 {
			q = totalQty - allocated
		}
		if q <= 0 {
			continue
		}
		allocated += q
		child := parent
		child.OrderID = fmt.Sprintf("%s-C%02d", parent.OrderID, i+1)
		child.ParentID = parent.OrderID
		child.OrigQty = q
		child.FilledQty = 0
		child.Status = common.Init
		child.CreatedAt = now + int64(i)*intervalMs
		child.UpdatedAt = child.CreatedAt
		children = append(children, child)
	}
	return children
}

// weightsFor 生成各切片下单比例（和为 1）。
func weightsFor(algo Algo, n int) []float64 {
	w := make([]float64, n)
	switch algo {
	case VWAP:
		// U 型日内成交量分布：开盘/收盘放量，中间缩量
		var sum float64
		for i := 0; i < n; i++ {
			x := float64(i) / float64(maxInt(n-1, 1))
			v := 0.5 + 2.0*(x-0.5)*(x-0.5)
			w[i] = v
			sum += v
		}
		for i := range w {
			w[i] /= sum
		}
	case Adaptive, TWAP:
		// 均匀切片（自适应在真实环境按盘口深度动态调整，这里基线均匀）
		for i := range w {
			w[i] = 1.0 / float64(n)
		}
	}
	return w
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
