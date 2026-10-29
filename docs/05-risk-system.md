# 05 · 矩阵式三层风控体系

> 九坤风控最大特点：**事前拦截 > 事中监控 > 事后熔断，三层不可逆**。
> 违规订单直接丢弃、不进柜台；所有拦截写 `risk_log` 可审计。

---

## 1. 风控矩阵总览

```
                    ┌─────────────────────────────┐
                    │        合规委托 (放行)         │
                    └───────────────┬───────────────┘
                                    │
        ┌───────────────────────────┼───────────────────────────┐
        ▼                           ▼                           ▼
 ┌─────────────┐            ┌─────────────┐            ┌─────────────┐
 │ 事前 PRE     │            │ 事中 INTRA  │            │ 事后 POST   │
 │ 100% 拦截   │──监控触发──▶│ 毫秒级监控  │──熔断触发──▶│ 回撤止损   │
 │ 不进柜台    │            │ 告警/降仓   │            │ 自动关停   │
 └─────────────┘            └─────────────┘            └─────────────┘
```

---

## 2. 事前风控（下单前 100% 拦截）

> 实现：`go/internal/risk/pre_trade.go`，规则预载 Redis `quant:risk:rule:*`，O(1) 判断。

| 维度 | 规则 | 阈值来源 | 违规动作 |
|------|------|----------|----------|
| 单票仓位 | `qty*price / total ≤ 上限` | `quant:risk:param:global` | REJECT |
| 行业暴露 | `行业市值 / total ≤ 上限` | 实时 `quant:risk:exposure:industry` | REJECT |
| 单日亏损 | `当日 pnl ≤ -max_loss` | `quant:risk:pnl:day` | REJECT + HALT |
| 策略回撤 | `当日回撤 ≤ -dd_threshold` | `quant:risk:drawdown:strategy:{sid}` | REJECT + HALT |
| 涨跌停 | `GETBIT limit:up/down` | Bitmap | REJECT |
| ST / 停牌 | `SISMEMBER blacklist` | Set | REJECT |
| 单笔手数 | `qty ≤ max_lot` | 规则 | REJECT |
| 杠杆率 | `gross/total ≤ lev` | 资金表 | REJECT |
| 集中度 | `topN 占比 ≤ cap` | 持仓 | REJECT |

**代码骨架**
```go
func (e *PreTradeEngine) Check(o Order, ctx *RiskCtx) (bool, string) {
    if o.Side == Buy && getbit(LimitUp, o.Symbol) == 1 { return false, "涨停禁买" }
    if isMember(Blacklist, o.Symbol) { return false, "黑名单/ST/停牌" }
    if exposure(o.Symbol) + o.Notional() > maxPosition { return false, "超单票仓位" }
    if industryExp(o.Industry) + o.Notional() > maxIndustry { return false, "超行业暴露" }
    if dayPnl() < -maxLoss { return false, "超单日亏损" }
    if turnover(o.Strategy) > maxTurnover { return false, "超换手上限" }
    return true, ""
}
```

---

## 3. 事中实时风控（毫秒级监控）

> 实现：`go/internal/risk/intraday.go`，订阅 Kafka 成交 + 盘口事件，实时刷新 Redis 指标。

- **净值回撤监控**：`quant:risk:pnl:day` 跌破阈值 → 告警
- **行业 / 风格 / 因子暴露**：每秒重算 `quant:risk:exposure:industry`
- **异常行情熔断**：盘口瞬时涨跌超阈值（闪崩 / 暴涨暴跌）→ 冻结相关标的
- **换手率超限**：`INCR quant:trade:freq:{sid}` 超阈值 → 降频 / 暂停

**熔断触发示例**
```go
if abs(priceJump5s) > crashThreshold {
    freezePool(symbolsInSector)         // 批量冻结
    setSwitch(strategy, HALT)
    emitKafka(risk.alert, "crash_halt")
}
```

---

## 4. 事后风控 & 止损

> 实现：`go/internal/risk/post_trade.go` + 盘后 `ledger.reconcile`。

- 日终绩效归因 → `strategy_perf`
- 策略超额回撤自动关停：`drawdown > threshold` → 次日 `switch=halt`
- 连续亏损降仓：N 日连亏 → 仓位系数 ×0.5
- 风控日志全量审计：`risk_log` 不可篡改

---

## 5. 风控开关与灰度

| switch 值 | 含义 | 行为 |
|-----------|------|------|
| `on` | 正常 | 全流量 |
| `gray` | 灰度 | 小仓位（如 10%）验证 |
| `off` | 关闭 | 不再出新信号 |
| `halt` | 熔断 | 立即关停 + 现有持仓择机平仓 |

> 一键关停：调度中心置 `quant:risk:switch:*` = halt，执行层订阅后立即停止拆单与上报。

---

## 6. 监控指标（Kafka → 监控面板）

| 指标 | 来源 | 告警 |
|------|------|------|
| 下单延迟 P99 | `quant:latency:order` | > 1ms |
| 实时回撤 | `quant:risk:pnl:day` | 超阈值 |
| 行业暴露 | `quant:risk:exposure:industry` | 超阈值 |
| 拦截率 | `risk_log` 计数 | 突增告警 |
| 策略夏普 | `quant:metric:sharpe:{sid}` | 滚动转负 |
