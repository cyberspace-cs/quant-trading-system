-- ============================================================================
-- quant-trading-system · MySQL 工业级建表（对标九坤/明汯/幻方）
-- 适用 MySQL 8.0+  /  TiDB 兼容
-- 约定：utf8mb4 / DATETIME(3) 毫秒 / DECIMAL(20,4) 金额 / 只追加流水
-- ============================================================================

SET NAMES utf8mb4;
CREATE DATABASE IF NOT EXISTS quant CHARACTER SET utf8mb4 COLLATE utf8mb4_general_ci;
USE quant;

-- ---------------------------------------------------------------------------
-- 1. 维度表（静态 / 低频变更）
-- ---------------------------------------------------------------------------

-- 股票基础信息 / 行业 / 板块
DROP TABLE IF EXISTS dim_stock;
CREATE TABLE dim_stock (
  stock_code   VARCHAR(16)  NOT NULL,
  stock_name   VARCHAR(64)  NOT NULL,
  industry     VARCHAR(32)  NOT NULL DEFAULT '',
  industry_code VARCHAR(16) NOT NULL DEFAULT '',
  board        VARCHAR(16)  NOT NULL DEFAULT '',        -- 主板/创业板/科创板/北交
  list_date    DATE         NOT NULL,
  delist_date  DATE         DEFAULT NULL,
  is_st        TINYINT      NOT NULL DEFAULT 0,
  int_id       INT          NOT NULL,                  -- 稳定整数ID（Redis Bitmap 用）
  updated_at   DATETIME(3)  NOT NULL,
  PRIMARY KEY (stock_code),
  KEY idx_industry (industry_code),
  KEY idx_int_id (int_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='股票维度表';

-- 策略元数据 / 参数模板
DROP TABLE IF EXISTS dim_strategy;
CREATE TABLE dim_strategy (
  strategy_id   VARCHAR(32)  NOT NULL,
  strategy_name VARCHAR(64)  NOT NULL,
  strategy_type VARCHAR(32)  NOT NULL,                 -- multi_factor/intraday/arb/ai
  params        JSON         NOT NULL,                 -- 参数模板
  weight        DECIMAL(10,4) NOT NULL DEFAULT 1.0000, -- 策略权重
  status        TINYINT      NOT NULL DEFAULT 1,       -- 1 on / 0 off
  created_at    DATETIME(3)  NOT NULL,
  updated_at    DATETIME(3)  NOT NULL,
  PRIMARY KEY (strategy_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='策略维度表';

-- 风控规则模板
DROP TABLE IF EXISTS dim_risk_rule;
CREATE TABLE dim_risk_rule (
  rule_id      VARCHAR(32)  NOT NULL,
  dimension    VARCHAR(32)  NOT NULL,                  -- position/industry/pnl/drawdown/limit/leverage
  comparator   VARCHAR(8)   NOT NULL,                  -- <= >= == 
  threshold    DECIMAL(20,4) NOT NULL,
  action       VARCHAR(16)  NOT NULL,                  -- REJECT/WARN/HALT
  priority     INT          NOT NULL DEFAULT 100,
  enabled      TINYINT      NOT NULL DEFAULT 1,
  created_at   DATETIME(3)  NOT NULL,
  updated_at   DATETIME(3)  NOT NULL,
  PRIMARY KEY (rule_id),
  KEY idx_dim (dimension)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='风控规则模板';

-- 因子元数据
DROP TABLE IF EXISTS factor_meta;
CREATE TABLE factor_meta (
  factor_id    VARCHAR(32)  NOT NULL,
  factor_name  VARCHAR(64)  NOT NULL,
  category     VARCHAR(32)  NOT NULL,                  -- value/growth/quality/momentum/ai
  formula      VARCHAR(256) DEFAULT '',
  layer        TINYINT      NOT NULL DEFAULT 1,         -- 1 原始 2 中性化 3 合成
  created_at   DATETIME(3)  NOT NULL,
  PRIMARY KEY (factor_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='因子元数据';

-- ---------------------------------------------------------------------------
-- 2. 业务流水表（强一致 / 不可变）
-- ---------------------------------------------------------------------------

-- 持仓表（每日快照 + 实时增量）
DROP TABLE IF EXISTS position_snapshot;
CREATE TABLE position_snapshot (
  id            BIGINT       NOT NULL AUTO_INCREMENT,
  account_id    VARCHAR(32)  NOT NULL,
  symbol        VARCHAR(16)  NOT NULL,
  biz_date      DATE         NOT NULL,
  qty           BIGINT       NOT NULL DEFAULT 0,
  frozen_qty    BIGINT       NOT NULL DEFAULT 0,
  avg_cost      DECIMAL(20,4) NOT NULL DEFAULT 0,
  market_value  DECIMAL(20,4) NOT NULL DEFAULT 0,
  pnl           DECIMAL(20,4) NOT NULL DEFAULT 0,
  updated_at    DATETIME(3)  NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uk_acc_sym_date (account_id, symbol, biz_date),
  KEY idx_symbol (symbol),
  KEY idx_acc_date (account_id, biz_date)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='持仓快照';

-- 订单主表（不可改流水；按月分表示例）
DROP TABLE IF EXISTS order_main;
CREATE TABLE order_main (
  order_id      VARCHAR(40)  NOT NULL,
  account_id    VARCHAR(32)  NOT NULL,
  strategy_id   VARCHAR(32)  NOT NULL,
  symbol        VARCHAR(16)  NOT NULL,
  side          TINYINT      NOT NULL,                 -- 1 买 / -1 卖
  order_type    TINYINT      NOT NULL DEFAULT 1,       -- 1 限价 / 2 市价
  orig_qty      BIGINT       NOT NULL,
  filled_qty    BIGINT       NOT NULL DEFAULT 0,
  price         DECIMAL(20,4) DEFAULT NULL,
  status        TINYINT      NOT NULL DEFAULT 0,        -- 0 INIT 1 SENT 2 PARTIAL 3 FILLED 4 CANCELLED 5 REJECTED
  reject_reason VARCHAR(128)  DEFAULT NULL,
  parent_id     VARCHAR(40)  DEFAULT NULL,             -- 母单（拆单）
  created_at    DATETIME(3)  NOT NULL,
  updated_at    DATETIME(3)  NOT NULL,
  PRIMARY KEY (order_id),
  KEY idx_acc_date (account_id, created_at),
  KEY idx_symbol_date (symbol, created_at),
  KEY idx_parent (parent_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='订单主表';

-- 成交明细（逐笔）
DROP TABLE IF EXISTS fill_detail;
CREATE TABLE fill_detail (
  fill_id       VARCHAR(40)  NOT NULL,
  order_id      VARCHAR(40)  NOT NULL,
  symbol        VARCHAR(16)  NOT NULL,
  side          TINYINT      NOT NULL,
  filled_qty    BIGINT       NOT NULL,
  fill_price    DECIMAL(20,4) NOT NULL,
  fill_amount   DECIMAL(20,4) NOT NULL,
  fill_time     DATETIME(3)  NOT NULL,
  PRIMARY KEY (fill_id),
  KEY idx_order (order_id),
  KEY idx_symbol_date (symbol, fill_time)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='成交明细';

-- 资金表
DROP TABLE IF EXISTS capital_account;
CREATE TABLE capital_account (
  account_id    VARCHAR(32)  NOT NULL,
  available     DECIMAL(20,4) NOT NULL DEFAULT 0,
  frozen        DECIMAL(20,4) NOT NULL DEFAULT 0,
  total         DECIMAL(20,4) NOT NULL DEFAULT 0,
  nav           DECIMAL(20,4) NOT NULL DEFAULT 0,
  updated_at    DATETIME(3)  NOT NULL,
  PRIMARY KEY (account_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='资金账户';

-- 风控日志（审计，不可篡改）
DROP TABLE IF EXISTS risk_log;
CREATE TABLE risk_log (
  log_id        BIGINT       NOT NULL AUTO_INCREMENT,
  order_id      VARCHAR(40)  DEFAULT NULL,
  strategy_id   VARCHAR(32)  DEFAULT NULL,
  rule_id       VARCHAR(32)  DEFAULT NULL,
  dimension     VARCHAR(32)  NOT NULL,
  action        VARCHAR(16)  NOT NULL,
  reason        VARCHAR(256) NOT NULL,
  created_at    DATETIME(3)  NOT NULL,
  PRIMARY KEY (log_id),
  KEY idx_rule (rule_id),
  KEY idx_created (created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='风控日志';

-- 策略绩效
DROP TABLE IF EXISTS strategy_perf;
CREATE TABLE strategy_perf (
  id            BIGINT       NOT NULL AUTO_INCREMENT,
  strategy_id   VARCHAR(32)  NOT NULL,
  biz_date      DATE         NOT NULL,
  turnover      DECIMAL(20,4) NOT NULL DEFAULT 0,
  pnl           DECIMAL(20,4) NOT NULL DEFAULT 0,
  sharpe        DECIMAL(10,4) NOT NULL DEFAULT 0,
  max_drawdown  DECIMAL(10,4) NOT NULL DEFAULT 0,
  win_rate      DECIMAL(10,4) NOT NULL DEFAULT 0,
  profit_factor DECIMAL(10,4) NOT NULL DEFAULT 0,
  PRIMARY KEY (id),
  UNIQUE KEY uk_sid_date (strategy_id, biz_date),
  KEY idx_date (biz_date)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='策略绩效';

-- 因子值（日级，回测/研究数据源）
DROP TABLE IF EXISTS factor_value;
CREATE TABLE factor_value (
  id            BIGINT       NOT NULL AUTO_INCREMENT,
  symbol        VARCHAR(16)  NOT NULL,
  factor_id     VARCHAR(32)  NOT NULL,
  biz_date      DATE         NOT NULL,
  value         DECIMAL(20,6) NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uk_sym_fac_date (symbol, factor_id, biz_date),
  KEY idx_date (biz_date)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='因子值日级';
