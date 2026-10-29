package ledger

import "testing"

// TestReconcile 三账合一：差异可检出，一致时 ok
func TestReconcile(t *testing.T) {
	exp := map[string]int64{"600000": 100, "600519": 200}
	act := map[string]int64{"600000": 100, "600519": 150}

	diffs, ok := Reconcile(exp, act)
	if ok {
		t.Fatal("存在差异时应返回 ok=false")
	}
	if len(diffs) != 1 || diffs[0].Symbol != "600519" || diffs[0].Expected-diffs[0].Actual != 50 {
		t.Fatalf("差异计算错误: %+v", diffs)
	}

	if _, ok := Reconcile(map[string]int64{"A": 1}, map[string]int64{"A": 1}); !ok {
		t.Fatal("一致时应返回 ok=true")
	}
}
