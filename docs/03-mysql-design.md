# 03 · MySQL 详细设计（强一致兜底）

> 所有账务、审计、规则、静态配置、流水台账 → MySQL 强一致、可审计、不可丢失。
> 完整 DDL 见 [`../sql/01_schema.sql`](../sql/01_schema.sql)，初始化数据见 [`../sql/02_init_data.sql`](../sql/02_init_data.sql)。

---

## 1. 设计原则

1. **强一致**：账务流水（订单 / 成交 / 资金）用 InnoDB + 事务，不可丢失、不可篡改。
2. **只存静态 + 低频变更 + 审计数据**：实时热数据在 Redis，落库用于持久化与盘后对账。
3. **不可变流水**：订单表、成交明细表、风控日志表 **只追加不更新**（状态变更用新行或状态字段 + 版本）。
4. **分表策略**：订单 / 成交按 `trade_date` 或 `symbol` 哈希分 64~256 张子表；绩效 / 因子按日分区。
5. **字符集**：`utf8mb4`；时间统一 `DATETIME(3)` 毫秒；金额用 `DECIMAL(20,4)`，禁止 float。
6. **索引**：查询维度建联合索引（account_id, trade_date）、（symbol, trade_date）。

---

## 2. 核心表清单

| 表 | 作用 | 关键字段 | 写入频率 |
|----|------|----------|----------|
| `dim_stock` | 股票基础信息 / 行业 / 指数成分 | stock_code, name, industry, board, list_date | 低频 |
| `dim_strategy` | 策略元数据 / 参数模板 | strategy_id, name, type, params, status | 低频 |
| `dim_risk_rule` | 风控规则模板 | rule_id, dimension, threshold, action | 低频 |
| `factor_meta` | 因子元数据 | factor_id, name, category, formula | 低频 |
| `position_snapshot` | 持仓每日快照 + 实时增量 | account_id, symbol, qty, avg_cost, pnl | 实时 |
| `order_main` | 订单主表（不可改流水） | order_id, symbol, side, qty, price, status | 实时 |
| `fill_detail` | 成交明细（逐笔） | fill_id, order_id, symbol, filled_qty, fill_price | 实时 |
| `capital_account` | 资金表 | account_id, available, frozen, total, nav | 实时 |
| `risk_log` | 风控日志（审计） | log_id, order_id, rule_id, reason, action | 实时 |
| `strategy_perf` | 策略绩效 | strategy_id, date, sharpe, max_dd, win_rate | 日级 |
| `factor_value` | 因子值（日级，回测/研究源） | symbol, factor_id, trade_date, value | 日级 |

---

## 3. 关键表结构（节选，完整见 SQL）

### 3.1 持仓表 `position_snapshot`
```sql
CREATE TABLE position_snapshot (
  id            BIGINT      PRIMARY KEY AUTO_INCREMENT,
  account_id    VARCHAR(32) NOT NULL,
  symbol        VARCHAR(16) NOT NULL,
  biz_date      DATE        NOT NULL,
  qty           BIGINT      NOT NULL DEFAULT 0,     -- 持仓数量（股）
  frozen_qty    BIGINT      NOT NULL DEFAULT 0,     -- 冻结数量
  avg_cost      DECIMAL(20,4) NOT NULL DEFAULT 0,   -- 持仓成本
  market_value  DECIMAL(20,4) NOT NULL DEFAULT 0,   -- 市值
  pnl           DECIMAL(20,4) NOT NULL DEFAULT 0,   -- 浮动盈亏
  updated_at    DATETIME(3) NOT NULL,
  UNIQUE KEY uk_acc_sym_date (account_id, symbol, biz_date),
  KEY idx_symbol (symbol)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
```

### 3.2 订单表 `order_main`（不可变流水）
```sql
CREATE TABLE order_main (
  order_id      VARCHAR(40) PRIMARY KEY,
  account_id    VARCHAR(32) NOT NULL,
  strategy_id   VARCHAR(32) NOT NULL,
  symbol        VARCHAR(16) NOT NULL,
  side          TINYINT     NOT NULL,   -- 1 买 / -1 卖
  order_type    TINYINT     NOT NULL,   -- 1 限价 / 2 市价
  orig_qty      BIGINT      NOT NULL,   -- 原始委托量
  filled_qty    BIGINT      NOT NULL DEFAULT 0,
  price         DECIMAL(20,4),
  status        TINYINT     NOT NULL,   -- 0 初始化 1 已报 2 部分成 3 已成 4 撤 5 废
  reject_reason VARCHAR(128),
  created_at    DATETIME(3) NOT NULL,
  updated_at    DATETIME(3) NOT NULL,
  KEY idx_acc_date (account_id, created_at),
  KEY idx_symbol_date (symbol, created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
```

### 3.3 成交明细 `fill_detail`
```sql
CREATE TABLE fill_detail (
  fill_id       VARCHAR(40) PRIMARY KEY,
  order_id      VARCHAR(40) NOT NULL,
  symbol        VARCHAR(16) NOT NULL,
  side          TINYINT     NOT NULL,
  filled_qty    BIGINT      NOT NULL,
  fill_price    DECIMAL(20,4) NOT NULL,
  fill_amount   DECIMAL(20,4) NOT NULL,
  fill_time     DATETIME(3) NOT NULL,
  KEY idx_order (order_id),
  KEY idx_symbol_date (symbol, fill_time)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
```

### 3.4 资金表 `capital_account`
```sql
CREATE TABLE capital_account (
  account_id    VARCHAR(32) PRIMARY KEY,
  available     DECIMAL(20,4) NOT NULL DEFAULT 0,  -- 可用资金
  frozen       DECIMAL(20,4) NOT NULL DEFAULT 0,  -- 冻结资金
  total         DECIMAL(20,4) NOT NULL DEFAULT 0,  -- 总资产
  nav           DECIMAL(20,4) NOT NULL DEFAULT 0,  -- 净值
  updated_at    DATETIME(3) NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
```

### 3.5 风控日志 `risk_log`（审计）
```sql
CREATE TABLE risk_log (
  log_id        BIGINT      PRIMARY KEY AUTO_INCREMENT,
  order_id      VARCHAR(40),
  strategy_id   VARCHAR(32),
  rule_id       VARCHAR(32),
  dimension     VARCHAR(32),   -- position/industry/pnl/drawdown/limit
  action        VARCHAR(16),   -- REJECT/WARN/HALT
  reason        VARCHAR(256),
  created_at    DATETIME(3) NOT NULL,
  KEY idx_rule (rule_id),
  KEY idx_created (created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
```

---

## 4. 一致性保障

- **唯一真值**：交易结果以券商柜台回执为准，成交回执驱动 `fill_detail` 与 `position_snapshot` 增量更新。
- **Redis 实时展示，MySQL 落地对账**：实时盈亏看 Redis，落库口径看 MySQL。
- **盘后三账合一**：`ledger.reconcile` 比对 持仓 / 资金 / 成交，差异告警并修正。
- **分表路由**：`order_main_${yyMM}` 按月，`fill_detail_${symbol_hash%64}` 按标的分片。

---

## 5. 性能与运维

| 项目 | 方案 |
|------|------|
| 读写分离 | 台账读走从库，实盘写走主库 |
| 归档 | 半年前 `fill_detail` / `order_main` 冷数据迁入 ClickHouse / OSS |
| 备份 | 主库每日全量 + binlog 增量，RPO < 5min |
| TiDB 选项 | 超大规模可上 TiDB 实现 HTAP + 弹性扩展 |
