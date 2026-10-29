package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"quant/internal/common"
	"quant/internal/data"
	"quant/internal/execution"
	"quant/internal/ledger"
	"quant/internal/risk"
	"quant/internal/strategy"
)

// ---------- 测试夹具：装配一套完整的最小系统 ----------

type fixture struct {
	srv   *Server
	state *AppState
	ts    *httptest.Server
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	symbols := []string{"600000", "600036", "000001", "600519", "300750", "000858", "601012"}
	industry := map[string]string{
		"600000": "银行", "600036": "银行", "000001": "银行",
		"600519": "白酒", "000858": "白酒", "300750": "新能源", "601012": "新能源",
	}
	cache := common.NewMemoryCache()
	bus := common.NewMemoryBus()
	book := ledger.NewPortfolio("ACC01", 100_000_000)
	oms := execution.NewOMS(cache, bus, execution.NewMockBroker(), book)

	intIDs := map[string]int{}
	for i, s := range symbols {
		intIDs[s] = 600000 + i
	}
	fc := data.NewFactorCache(cache)
	fc.InitPool(symbols, []string{"ST002"}, intIDs)

	pre := risk.NewPreTradeEngine(cache, book, intIDs)
	st := NewAppState("ACC01", 100_000_000, book, cache, bus, oms, pre, symbols, industry, intIDs)
	st.Strategies = []*StrategyInfo{
		{ID: "alpha_mf", Name: "中频多因子", Type: "multi_factor", Weight: 1, Status: "on"},
		{ID: "intraday_mr", Name: "日内均值回归", Type: "intraday", Weight: .5, Status: "gray"},
	}
	// 灌一笔行情，让下单有价格
	for _, s := range symbols {
		st.RecordTick(common.Tick{
			Symbol: s, Last: 20, Ts: common.NowMs(),
			Ask: [5]float64{20.01, 20.02, 20.03, 20.04, 20.05},
			Bid: [5]float64{19.99, 19.98, 19.97, 19.96, 19.95},
		}, map[string]float64{"momentum_20d": 0.01, "rev_5d": 0.02})
	}
	st.RecordEquity()

	srv := NewServer(st, "../../web")
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return &fixture{srv: srv, state: st, ts: ts}
}

// do 发起一次请求
func (f *fixture) do(t *testing.T, method, path, token string, body any, headers map[string]string) (int, map[string]any, http.Header) {
	t.Helper()
	var rdr *strings.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = strings.NewReader(string(b))
	} else {
		rdr = strings.NewReader("")
	}
	req, err := http.NewRequest(method, f.ts.URL+"/api/v1"+path, rdr)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out, resp.Header
}

func (f *fixture) login(t *testing.T, user, pw string) string {
	t.Helper()
	_, body, _ := f.do(t, "POST", "/auth/login", "", map[string]string{"username": user, "password": pw}, nil)
	data, _ := body["data"].(map[string]any)
	tok, _ := data["access_token"].(string)
	if tok == "" {
		t.Fatalf("login %s failed: %v", user, body)
	}
	return tok
}

// ---------- JWT ----------

func TestJWT(t *testing.T) {
	secret := []byte("test-secret")
	c := Claims{Sub: "U1", Name: "tester", Role: "trader", Typ: "access",
		Iat: time.Now().Unix(), Exp: time.Now().Add(time.Minute).Unix(), Jti: "abc"}

	tok := Sign(secret, c)
	got, err := Parse(secret, tok)
	if err != nil || got.Sub != "U1" || got.Role != "trader" {
		t.Fatalf("parse failed: %v %+v", err, got)
	}

	// 篡改载荷必须失败
	parts := strings.Split(tok, ".")
	if _, err := Parse(secret, parts[0]+"."+parts[1]+".AAAA"); err == nil {
		t.Fatal("tampered signature should fail")
	}
	// 换密钥必须失败
	if _, err := Parse([]byte("other"), tok); err == nil {
		t.Fatal("wrong secret should fail")
	}
	// 过期必须失败
	expired := Sign(secret, Claims{Sub: "U1", Exp: time.Now().Add(-time.Hour).Unix()})
	if _, err := Parse(secret, expired); err == nil {
		t.Fatal("expired token should fail")
	}
}

// ---------- 认证 / RBAC ----------

func TestAuthAndRBAC(t *testing.T) {
	f := newFixture(t)

	// 未登录
	code, body, _ := f.do(t, "GET", "/overview", "", nil, nil)
	if code != 401 || body["code"].(float64) != CodeUnauthorized {
		t.Fatalf("expected 401, got %d %v", code, body)
	}

	viewer := f.login(t, "viewer", "Viewer@123")
	trader := f.login(t, "trader", "Trader@123")

	// 只读角色可以看，不能下单
	if code, _, _ := f.do(t, "GET", "/overview", viewer, nil, nil); code != 200 {
		t.Fatalf("viewer read should pass, got %d", code)
	}
	code, body, _ = f.do(t, "POST", "/orders", viewer, map[string]any{
		"symbol": "600000", "side": "BUY", "qty": 100, "price": 20,
	}, nil)
	if code != 403 || body["code"].(float64) != CodeForbidden {
		t.Fatalf("viewer write should be 403, got %d %v", code, body)
	}

	// 交易员可以下单
	code, body, _ = f.do(t, "POST", "/orders", trader, map[string]any{
		"symbol": "600000", "side": "BUY", "qty": 100, "price": 20,
	}, nil)
	if code != 200 {
		t.Fatalf("trader order should pass, got %d %v", code, body)
	}

	// 错误口令
	if code, _, _ := f.do(t, "POST", "/auth/login", "", map[string]string{
		"username": "trader", "password": "wrong",
	}, nil); code != 401 {
		t.Fatalf("bad password should be 401, got %d", code)
	}
}

// ---------- 下单链路 ----------

func TestOrderLifecycle(t *testing.T) {
	f := newFixture(t)
	trader := f.login(t, "trader", "Trader@123")

	// 2000 股拆 3 片 → 每片 ≥500 股，柜台只成交 80% → 母单 PARTIAL
	code, body, _ := f.do(t, "POST", "/orders", trader, map[string]any{
		"symbol": "000001", "side": "BUY", "qty": 2000, "price": 20,
		"algo": "TWAP", "slices": 3, "strategy_id": "alpha_mf",
	}, nil)
	if code != 200 {
		t.Fatalf("create order failed: %d %v", code, body)
	}
	data := body["data"].(map[string]any)
	parent := data["parent"].(map[string]any)
	oid := parent["order_id"].(string)
	if parent["status"] != "PARTIAL" {
		t.Fatalf("expect PARTIAL, got %v", parent["status"])
	}
	children := data["children"].([]any)
	if len(children) != 3 {
		t.Fatalf("expect 3 children, got %d", len(children))
	}
	var sum float64
	for _, c := range children {
		sum += c.(map[string]any)["qty"].(float64)
	}
	if sum != 2000 {
		t.Fatalf("children qty sum=%v, want 2000", sum)
	}

	// 详情：子单 + 成交流水
	code, body, _ = f.do(t, "GET", "/orders/"+oid, trader, nil, nil)
	if code != 200 {
		t.Fatalf("detail failed: %d", code)
	}
	det := body["data"].(map[string]any)
	if len(det["children"].([]any)) != 3 || len(det["fills"].([]any)) == 0 {
		t.Fatalf("detail missing children/fills: %v", det)
	}
	if det["amendable"] != true {
		t.Fatal("PARTIAL order should be amendable")
	}

	// 改单
	if code, body, _ := f.do(t, "PUT", "/orders/"+oid, trader, map[string]any{
		"qty": 2000, "price": 20.5, "reason": "测试改价",
	}, nil); code != 200 {
		t.Fatalf("amend failed: %d %v", code, body)
	}

	// 撤单 + 重复撤单
	if code, _, _ := f.do(t, "DELETE", "/orders/"+oid, trader, nil, nil); code != 200 {
		t.Fatalf("cancel failed: %d", code)
	}
	if code, _, _ := f.do(t, "DELETE", "/orders/"+oid, trader, nil, nil); code != 409 {
		t.Fatalf("double cancel should be 409, got %d", code)
	}

	// 非法数量
	if code, _, _ := f.do(t, "POST", "/orders", trader, map[string]any{
		"symbol": "000001", "side": "BUY", "qty": 150, "price": 20,
	}, nil); code != 400 {
		t.Fatalf("odd lot should be 400, got %d", code)
	}
}

func TestRiskReject(t *testing.T) {
	f := newFixture(t)
	trader := f.login(t, "trader", "Trader@123")

	cases := []struct {
		name string
		body map[string]any
	}{
		{"超单笔手数", map[string]any{"symbol": "600000", "side": "BUY", "qty": 900000, "price": 20}},
		{"黑名单", map[string]any{"symbol": "ST002", "side": "BUY", "qty": 100, "price": 20}},
		{"超单票仓位", map[string]any{"symbol": "600000", "side": "BUY", "qty": 490000, "price": 20}},
	}
	for _, c := range cases {
		code, body, _ := f.do(t, "POST", "/orders", trader, c.body, nil)
		if code != 422 {
			t.Fatalf("%s: expect 422, got %d %v", c.name, code, body)
		}
		if body["code"].(float64) != CodeRiskRejected {
			t.Fatalf("%s: expect code %d, got %v", c.name, CodeRiskRejected, body["code"])
		}
	}
	if f.state.Book.RiskLogCount() < 3 {
		t.Fatalf("expect >=3 risk logs, got %d", f.state.Book.RiskLogCount())
	}
}

func TestIdempotency(t *testing.T) {
	f := newFixture(t)
	trader := f.login(t, "trader", "Trader@123")
	body := map[string]any{"symbol": "000001", "side": "BUY", "qty": 1000, "price": 20, "slices": 3}
	h := map[string]string{"Idempotency-Key": "unit-key-1"}

	code1, b1, _ := f.do(t, "POST", "/orders", trader, body, h)
	code2, b2, h2 := f.do(t, "POST", "/orders", trader, body, h)
	if code1 != 200 || code2 != 200 {
		t.Fatalf("codes %d %d", code1, code2)
	}
	id1 := b1["data"].(map[string]any)["parent"].(map[string]any)["order_id"]
	id2 := b2["data"].(map[string]any)["parent"].(map[string]any)["order_id"]
	if id1 != id2 {
		t.Fatalf("idempotency broken: %v vs %v", id1, id2)
	}
	if h2.Get("X-Idempotent-Replay") != "true" {
		t.Fatal("missing replay header")
	}
	if f.state.Book.OrderCount() != 4 { // 1 母单 + 3 子单
		t.Fatalf("expect 4 orders (no duplicate), got %d", f.state.Book.OrderCount())
	}
}

// ---------- 风控规则双人复核 ----------

func TestRuleApproval(t *testing.T) {
	f := newFixture(t)
	riskUser := f.login(t, "risk", "Risk@123")
	admin := f.login(t, "admin", "Admin@123")

	// 小幅收紧：立即生效
	code, body, _ := f.do(t, "PUT", "/risk/rules/R_POS", riskUser, map[string]any{
		"threshold": 0.075, "reason": "收紧仓位",
	}, nil)
	if code != 200 || body["data"].(map[string]any)["threshold"].(float64) != 0.075 {
		t.Fatalf("small change should apply: %d %v", code, body)
	}
	if f.state.PreRisk.MaxPositionRatio != 0.075 {
		t.Fatalf("engine not updated: %v", f.state.PreRisk.MaxPositionRatio)
	}

	// 大幅放宽：转待审批
	_, body, _ = f.do(t, "PUT", "/risk/rules/R_POS", riskUser, map[string]any{
		"threshold": 0.30, "reason": "放宽",
	}, nil)
	if body["data"].(map[string]any)["status"] != "pending_approval" {
		t.Fatalf("large change should be pending: %v", body)
	}

	// 自己不能审自己
	if code, _, _ := f.do(t, "POST", "/risk/rules/R_POS/approve", riskUser, nil, nil); code != 403 {
		t.Fatalf("self approve should be 403, got %d", code)
	}
	// 他人复核通过
	code, body, _ = f.do(t, "POST", "/risk/rules/R_POS/approve", admin, nil, nil)
	if code != 200 || body["data"].(map[string]any)["threshold"].(float64) != 0.30 {
		t.Fatalf("approve failed: %d %v", code, body)
	}
	if f.state.PreRisk.MaxPositionRatio != 0.30 {
		t.Fatalf("engine not updated after approve: %v", f.state.PreRisk.MaxPositionRatio)
	}
}

func TestParamVersionFlow(t *testing.T) {
	f := newFixture(t)
	trader := f.login(t, "trader", "Trader@123")
	riskUser := f.login(t, "risk", "Risk@123")

	// 低风险变更立即生效
	code, body, _ := f.do(t, "PUT", "/strategies/alpha_mf/params", trader, map[string]any{
		"params": map[string]float64{"rev_5d": 0.35}, "reason": "微调",
	}, nil)
	if code != 200 || body["data"].(map[string]any)["status"] != "active" {
		t.Fatalf("small param change should apply: %d %v", code, body)
	}
	// 高风险变更转待审批
	_, body, _ = f.do(t, "PUT", "/strategies/alpha_mf/params", trader, map[string]any{
		"params": map[string]float64{"rev_5d": 2.0}, "reason": "大幅提高",
	}, nil)
	if body["data"].(map[string]any)["status"] != "pending_approval" {
		t.Fatalf("large param change should be pending: %v", body)
	}
	ver := int(body["data"].(map[string]any)["version"].(float64))

	// 提交人不能自审
	if code, _, _ := f.do(t, "POST", "/strategies/alpha_mf/versions/"+itoa(ver)+"/approve", trader, nil, nil); code != 403 {
		t.Fatalf("self approve should be 403, got %d", code)
	}
	code, body, _ = f.do(t, "POST", "/strategies/alpha_mf/versions/"+itoa(ver)+"/approve", riskUser, nil, nil)
	if code != 200 || body["data"].(map[string]any)["status"] != "active" {
		t.Fatalf("approve version failed: %d %v", code, body)
	}
	if f.state.S.Params("alpha_mf")["rev_5d"] != 2.0 {
		t.Fatal("params not applied")
	}
}

// ---------- 分页 / 限流 / WebSocket ----------

func TestPagination(t *testing.T) {
	f := newFixture(t)
	trader := f.login(t, "trader", "Trader@123")
	for i := 0; i < 5; i++ {
		f.do(t, "POST", "/orders", trader, map[string]any{
			"symbol": "000001", "side": "BUY", "qty": 100, "price": 20,
		}, map[string]string{"Idempotency-Key": "pg-" + itoa(i)})
	}
	code, body, _ := f.do(t, "GET", "/orders?page=2&size=2", trader, nil, nil)
	if code != 200 {
		t.Fatalf("list failed: %d", code)
	}
	meta := body["meta"].(map[string]any)
	if meta["page"].(float64) != 2 || meta["size"].(float64) != 2 || meta["total"].(float64) != 5 {
		t.Fatalf("bad meta: %v", meta)
	}
	if len(body["data"].([]any)) != 2 {
		t.Fatalf("page size mismatch: %v", body["data"])
	}
}

func TestRateLimit(t *testing.T) {
	f := newFixture(t)
	f.srv.RLWrit = NewLimiter(1, 2) // 每秒 1 个、突发 2 个
	trader := f.login(t, "trader", "Trader@123")

	limited := false
	for i := 0; i < 10; i++ {
		code, _, _ := f.do(t, "POST", "/orders", trader, map[string]any{
			"symbol": "000001", "side": "BUY", "qty": 100, "price": 20,
		}, map[string]string{"Idempotency-Key": "rl-" + itoa(i)})
		if code == 429 {
			limited = true
			break
		}
	}
	if !limited {
		t.Fatal("expected 429 after burst exhausted")
	}
}

func TestWSAccept(t *testing.T) {
	// RFC6455 标准测试向量
	if got := wsAccept("dGhlIHNhbXBsZSBub25jZQ=="); got != "s3pPLMBiTxaQ9kYGzzhZRbK+xOo=" {
		t.Fatalf("bad accept: %s", got)
	}
}

func TestAuditWritten(t *testing.T) {
	f := newFixture(t)
	trader := f.login(t, "trader", "Trader@123")
	f.do(t, "POST", "/orders", trader, map[string]any{
		"symbol": "000001", "side": "BUY", "qty": 100, "price": 20,
	}, nil)
	if f.state.Audit.Count() == 0 {
		t.Fatal("write op should be audited")
	}
	logs := f.state.Audit.List(AuditQuery{Action: "order.create"})
	if len(logs) != 1 || logs[0].UserName != "trader" || logs[0].IP == "" {
		t.Fatalf("bad audit entry: %+v", logs)
	}
}

func TestOpenAPISpec(t *testing.T) {
	var spec map[string]any
	if err := json.Unmarshal(BuildOpenAPI(), &spec); err != nil {
		t.Fatalf("spec not valid json: %v", err)
	}
	if spec["openapi"] != "3.0.3" {
		t.Fatalf("bad version: %v", spec["openapi"])
	}
	paths := spec["paths"].(map[string]any)
	if len(paths) < 40 {
		t.Fatalf("expect >=40 paths, got %d", len(paths))
	}
}

// itoa 避免额外 import strconv
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

var _ = strategy.NewGenerator
