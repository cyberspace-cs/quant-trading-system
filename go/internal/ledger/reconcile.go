package ledger

import (
	"fmt"
	"sort"
)

// Diff 对账差异
type Diff struct {
	Symbol   string
	Expected int64 // 台账（柜台回执聚合，真值）
	Actual   int64 // 实时缓存 / 券商查询
}

// Reconcile 盘后三账合一：比对持仓/资金/成交的期望值与实际值。
// 返回差异列表与是否一致（ok=true 表示三账完全对齐）。
func Reconcile(expected, actual map[string]int64) ([]Diff, bool) {
	keys := map[string]struct{}{}
	for k := range expected {
		keys[k] = struct{}{}
	}
	for k := range actual {
		keys[k] = struct{}{}
	}
	var syms []string
	for k := range keys {
		syms = append(syms, k)
	}
	sort.Strings(syms)

	var diffs []Diff
	for _, s := range syms {
		e := expected[s]
		a := actual[s]
		if e != a {
			diffs = append(diffs, Diff{Symbol: s, Expected: e, Actual: a})
		}
	}
	return diffs, len(diffs) == 0
}

// Report 生成对账报告文本
func Report(diffs []Diff, ok bool) string {
	if ok {
		return "对账完成：三账合一，无差异 ✅"
	}
	msg := fmt.Sprintf("对账完成：发现 %d 处差异 ⚠️\n", len(diffs))
	for _, d := range diffs {
		msg += fmt.Sprintf("  %s 台账=%d 实际=%d 差额=%d\n", d.Symbol, d.Expected, d.Actual, d.Expected-d.Actual)
	}
	return msg
}
