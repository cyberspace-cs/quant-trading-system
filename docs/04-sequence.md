# 04 · 全链路时序流程（交易日完整跑通）

> 盘前 9:00–9:30 → 盘中 9:30–15:00 → 盘后 15:00 后，全链路闭环。

---

## 1. 盘前 9:00 – 9:30（初始化）

```
scheduler ──▶ 1. 初始化股票池 / 黑名单 / 涨跌停列表 写入 Redis
            ──▶ 2. 加载当日风控参数 / 策略权重 / 模型参数
            ──▶ 3. 重置当日计数器 / 亏损统计 / 换手统计
            ──▶ 4. 预热因子 / 预计算早盘基础特征
```

| 步骤 | 操作 | 写入 Key |
|------|------|----------|
| 1 | 拉 dim_stock + 风控黑名单 → Redis | `quant:pool:stock` `quant:pool:blacklist` `quant:limit:up/down` |
| 2 | 加载 dim_risk_rule / dim_strategy | `quant:risk:param:global` `quant:risk:switch:{sid}` |
| 3 | 清零计数 | `quant:trade:freq:*` `quant:risk:pnl:day` |
| 4 | 预计算 | `quant:factor:{code}:*` `quant:status:trading=pre→open` |

---

## 2. 盘中 9:30 – 15:00（主循环）

```
┌─────────────────────────── 每个行情 tick / 每秒 ───────────────────────────┐
│                                                                             │
│  data(Flink/Go)                                                             │
│    ├─ 清洗 Tick/K线 → 更新 Redis 行情&因子                                  │
│    │     HSET quant:tick:{code} ... ; SET quant:factor:{code}:{f} val       │
│    └─ 发布 Kafka 行情事件  topic=market.tick                                │
│                                                                             │
│  strategy(Go/Py)                                                            │
│    ├─ 批量拉取 Redis 因子 + 盘口                                            │
│    ├─ 模型打分 → 股票排序 → 权重 → 对比持仓 → 调仓信号                       │
│    └─ 发布 Kafka 信号事件  topic=strategy.signal                            │
│                                                                             │
│  risk(预交易)                                                               │
│    ├─ 读取实时持仓 / 资金 / 行业暴露 (Redis)                                 │
│    ├─ 事前规则 100% 拦截（仓位/行业/日亏/涨跌停/ST/手数）                     │
│    ├─ 违规 → 丢弃 + 写 risk_log + Kafka 风控事件                            │
│    └─ 合规 → 下发执行层                                                     │
│                                                                             │
│  execution(算法交易)                                                        │
│    ├─ 智能拆单 TWAP/VWAP → 切片                                              │
│    ├─ 订单状态机：INIT→SENT→PARTIAL→FILLED/CANCELLED                        │
│    ├─ 下发券商柜台（FIX / 原生 API）                                         │
│    └─ 回执 → 更新 Redis 持仓/资金 + Kafka 成交事件                          │
│                                                                             │
│  ledger(异步)                                                               │
│    ├─ 成交回执 → MySQL fill_detail / order_main / position_snapshot         │
│    └─ 实时净值 / 盈亏 / 风控指标刷新                                        │
│                                                                             │
│  monitor                                                                     │
│    └─ Kafka 全链路日志落盘 + 实时监控告警（延迟/夏普/回撤）                   │
└─────────────────────────────────────────────────────────────────────────────┘
```

**关键事件流（Kafka）**
```
market.tick  → strategy.signal → risk.check → order.send
                                                  → order.fill (柜台回执)
                                                  → risk.alert (事中触发)
```

---

## 3. 盘后 15:00 后（清算归因）

```
scheduler ──▶ 1. 停止所有策略、关停下单通道（risk:switch=halt）
           ──▶ 2. 全量对账：订单 / 成交 / 持仓 / 资金（三账合一）
           ──▶ 3. 批量更新日级因子、模型样本（factor_value）
           ──▶ 4. 绩效归因：夏普 / 最大回撤 / 胜率 / 因子收益拆解
           ──▶ 5. 冷数据归档 / 日志备份 / 模型版本保存
           ──▶ 6. 生成迭代报告 → 研究侧（回测环境）下一轮
```

---

## 4. 异常与容灾时序

| 场景 | 处理 |
|------|------|
| 行情中断 | 暂停新信号，沿用最后快照；超时 N 秒 → 策略熔断 |
| 柜台连接失败 | 订单状态置 `PENDING`，重连后补单 / 撤单 |
| Redis 抖动 | 本地缓存兜底 + Kafka 重放；不阻断下单关键路径 |
| 单策略回撤超限 | 事中触发 `risk:switch={sid}=halt`，自动关停 |
| 极端行情 | 波动率阈值触发批量冻结标的池（limit bitmap 全置） |

---

## 5. 状态机（订单全生命周期）

```
        ┌─────────┐
 new ──▶│ INIT    │
        └────┬────┘
             │ send
             ▼
        ┌─────────┐   fill(all)   ┌─────────┐
        │ SENT    │──────────────▶│ FILLED  │
        └────┬────┘               └─────────┘
             │ fill(partial)
             ▼
        ┌─────────┐  cancel  ┌──────────┐
        │ PARTIAL │─────────▶│CANCELLED │
        └────┬────┘          └──────────┘
             │ reject
             ▼
        ┌─────────┐
        │ REJECTED│ (风控/柜台废单)
        └─────────┘
```
状态枚举：`INIT / SENT / PARTIAL / FILLED / CANCELLED / REJECTED`
