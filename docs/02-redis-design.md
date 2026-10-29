# 02 · Redis 详细设计（量化绝对核心）

> 所有低延迟、高并发、状态热数据全部放 Redis。
> 本文件给出**可直接写进简历的键设计、数据结构、TTL、内存与最佳实践**。

---

## 1. 设计原则

1. **Key 命名空间统一前缀** `quant:`，按业务域分区。
2. **只放热数据**：盘中高频读写的行情 / 因子 / 持仓 / 订单 / 风控；静态配置走 MySQL。
3. **毫秒级更新**：行情、盘口、因子值；**日级过期**：计数器、股票池盘后清理。
4. **内存可控**：Tick 只留最新 N 档盘口；因子值用 `String` + 二进制/JSON；Bitmap 做涨跌停过滤。
5. **强一致兜底在 MySQL**：Redis 只做实时展示，盘后由台账校准。

---

## 2. 键设计总表

### 2.1 数据中台 · 行情 / 因子 / 股票池

| Key | 类型 | 说明 | TTL | 写入方 |
|-----|------|------|-----|--------|
| `quant:tick:{code}` | Hash | 最新盘口：last/ask1~ask5/bid1~bid5/vol/timestamp | 常驻(盘后清) | data |
| `quant:tick:stream:{code}` | Stream | 逐笔成交流（保留最近 N 条，可回放） | 1d | data |
| `quant:factor:{code}:{factor_name}` | String | 单股票实时因子值（JSON/浮点） | 常驻 | data |
| `quant:factor:score:{code}` | String | 模型合成打分 | 常驻 | strategy |
| `quant:pool:stock` | Set | 当日可交易股票池 | 1d | data(盘前) |
| `quant:pool:blacklist` | Set | 黑名单股票（ST/停牌/风控禁买） | 1d | risk(盘前) |
| `quant:limit:up` | Bitmap | 涨停股票位图，快速过滤不可买标的 | 1d | data |
| `quant:limit:down` | Bitmap | 跌停股票位图，快速过滤不可卖标的 | 1d | data |
| `quant:status:trading` | String | 全局交易日状态：pre/open/mid/close | 1d | scheduler |
| `quant:industry:{code}` | String | 股票所属行业（中性化用） | 1d | data |

> **Bitmap 用法**：`SETBIT quant:limit:up <code_int_id> 1`；过滤时 `GETBIT` 判断是否涨停，O(1) 且省内存。
> `code_int_id` 由股票代码映射的稳定整数（如 `600000 → 600000`）。

### 2.2 风控层 · 阈值 / 计数器 / 状态

| Key | 类型 | 说明 | TTL |
|-----|------|------|-----|
| `quant:risk:rule:{rule_id}` | Hash | 单条风控规则（维度/阈值/开关） | 常驻 |
| `quant:risk:param:global` | Hash | 全局风控参数：单票上限/行业上限/日亏上限/回撤阈值 | 常驻 |
| `quant:risk:exposure:industry` | Hash | 实时行业暴露度 `field=行业 val=市值占比` | 盘中日更 |
| `quant:risk:pnl:day` | String | 当日累计盈亏（事中回撤监控） | 1d |
| `quant:risk:drawdown:strategy:{sid}` | String | 单策略当日回撤 | 1d |
| `quant:risk:switch:{sid}` | String | 策略开关：on/off/gray（灰度）/halt（熔断） | 常驻 |
| `quant:risk:turnover:{sid}` | String(INCR) | 策略当日换手计数器（防超额交易） | 1d |

### 2.3 交易执行层 · 订单 / 持仓 / 频率

| Key | 类型 | 说明 | TTL |
|-----|------|------|-----|
| `quant:order:live:{order_id}` | Hash | 在途订单：status/remain_qty/price/filled_qty/code | 盘后清 |
| `quant:order:strategy:{sid}` | Set | 策略在途订单集合（批量撤单用） | 盘后清 |
| `quant:position:real:{code}` | Hash | 实时持仓：qty/cost/avg_price/pnl/frozen | 盘后清 |
| `quant:position:account` | Hash | 账户总持仓市值 / 可用 / 冻结 | 盘后清 |
| `quant:trade:freq:{strategy}` | String(INCR) | 策略当日换手计数器，防超额交易 | 1d |
| `quant:capital:available` | String | 可用资金 | 盘后清 |
| `quant:capital:frozen` | String | 冻结资金（已报未成占用） | 盘后清 |
| `quant:capital:total` | String | 总资产 / 净值 | 盘后清 |

### 2.4 运行状态 / 监控

| Key | 类型 | 说明 |
|-----|------|------|
| `quant:node:{svc}:heartbeat` | String | 各服务心跳（可用性监控） |
| `quant:latency:order` | Stream | 下单延迟采样（P50/P99 监控） |
| `quant:metric:sharpe:{sid}` | String | 滚动夏普（事中展示） |

---

## 3. 典型读写示例

### 3.1 盘前预热（data + risk）
```bash
# 股票池 & 黑名单
SADD quant:pool:stock 600000 600036 000001 ...
SADD quant:pool:blacklist ST股票 停牌股票 ...
# 涨跌停位图
SETBIT quant:limit:up 600000 0
SETBIT quant:limit:down 600000 0
# 全局交易日状态
SET quant:status:trading open
# 风控开关初始化
SET quant:risk:switch:alpha on
```

### 3.2 策略实时读因子 + 盘口
```bash
# 读某票最新盘口
HGETALL quant:tick:600000
# 读某票因子值
GET quant:factor:600000:momentum_20d
# 判断是否可买（非涨停 + 在股票池 + 非黑名单）
GETBIT quant:limit:up 600000   # 1=涨停不可买
SISMEMBER quant:pool:blacklist 600000
```

### 3.3 事前风控计数（原子）
```bash
# 策略当日换手 +1，超限则拒绝
INCR quant:trade:freq:alpha
# 实时更新在途订单
HSET quant:order:live:{oid} status PARTIAL filled_qty 300 remain_qty 700
```

---

## 4. 内存 / 性能 / 高可用

| 关注点 | 方案 |
|--------|------|
| 低延迟 | 单线程模型 + 内存读写，盘口/因子读 P99 < 1ms |
| 大 Key | Tick Hash 只存 5 档盘口 + last，避免大 Hash |
| 热点 | 行情热点 Key 做 **客户端本地缓存 + 短 TTL** 双写 |
| 持久化 | 关闭 AOF 或 `everysec`；Redis 仅热数据，丢失可由 Kafka 重放 + MySQL 校准 |
| 高可用 | Redis Sentinel / Cluster；交易核心 Key 放独立实例，避免与研究侧混用 |
| 容量 | 全市场 5000 票 × 因子 200 个 × 平均 40B ≈ 40MB，内存压力极小 |
| 隔离 | **回测 / 仿真 / 实盘** 使用不同 Redis 库（db0/db1/db2）或命名空间前缀 |

---

## 5. 三环境隔离的 Redis 约定

| 环境 | 前缀 / DB | 说明 |
|------|-----------|------|
| 回测 backtest | `quant_bt:` / db0 | 历史重放，因子全量预载 |
| 仿真 paper | `quant_pa:` / db1 | 仿真撮合，独立订单簿 |
| 实盘 live | `quant:` / db2 | 真实行情与持仓，强管控 |

> 通过配置切换 `REDIS_KEY_PREFIX` 实现代码零改动的环境隔离。
