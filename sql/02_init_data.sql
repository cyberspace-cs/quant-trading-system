-- ============================================================================
-- quant-trading-system · 初始化数据
-- 演示用样本：股票/策略/风控规则/资金账户
-- ============================================================================

USE quant;

-- 1. 股票维度（含 int_id 供 Redis Bitmap）
INSERT INTO dim_stock (stock_code, stock_name, industry, industry_code, board, list_date, is_st, int_id, updated_at) VALUES
('600000', '浦发银行', '银行',   '801780', '主板',   '1999-11-10', 0, 600000, NOW(3)),
('600036', '招商银行', '银行',   '801780', '主板',   '2002-04-09', 0, 600036, NOW(3)),
('000001', '平安银行', '银行',   '801780', '主板',   '1991-04-03', 0, 000001, NOW(3)),
('600519', '贵州茅台', '白酒',   '801120', '主板',   '2001-08-27', 0, 600519, NOW(3)),
('300750', '宁德时代', '新能源', '801730', '创业板', '2018-06-11', 0, 300750, NOW(3)),
('000858', '五粮液',   '白酒',   '801120', '主板',   '1998-04-27', 0, 000858, NOW(3)),
('601012', '隆基绿能', '新能源', '801730', '主板',   '2012-04-11', 0, 601012, NOW(3)),
('ST002',  '示例ST',   '其他',   '801000', '主板',   '2010-01-01', 1, 002002, NOW(3));

-- 2. 策略维度
INSERT INTO dim_strategy (strategy_id, strategy_name, strategy_type, params, weight, status, created_at, updated_at) VALUES
('alpha_mf',  '中频多因子选股', 'multi_factor', '{"top_n":100,"rebalance":"daily","neutralize":"industry,size"}', 1.0000, 1, NOW(3), NOW(3)),
('intraday_mr','日内均值回归',   'intraday',     '{"window":30,"holding":300}',                                 0.5000, 1, NOW(3), NOW(3)),
('ai_lstm',   'AI-LSTM预测',     'ai',           '{"hidden":64,"seq_len":20}',                                  0.8000, 1, NOW(3), NOW(3));

-- 3. 风控规则模板（九坤矩阵式三层）
INSERT INTO dim_risk_rule (rule_id, dimension, comparator, threshold, action, priority, enabled, created_at, updated_at) VALUES
('R_POS',    'position',   '<=', 0.0800, 'REJECT', 100, 1, NOW(3), NOW(3)),  -- 单票<=8%
('R_IND',    'industry',   '<=', 0.3000, 'REJECT', 100, 1, NOW(3), NOW(3)),  -- 行业<=30%
('R_LOSS',   'pnl',        '>=', -0.0200,'HALT',   200, 1, NOW(3), NOW(3)),  -- 日亏>=-2% 熔断
('R_DD',     'drawdown',   '>=', -0.0300,'HALT',   200, 1, NOW(3), NOW(3)),  -- 回撤>=-3% 熔断
('R_LOT',    'lot',        '<=', 500000, 'REJECT', 50,  1, NOW(3), NOW(3)),  -- 单笔手数<=50万
('R_LEV',    'leverage',   '<=', 1.0000, 'REJECT', 100, 1, NOW(3), NOW(3)),  -- 杠杆<=1（不许裸卖空）
('R_LIMIT',  'limit',      '==', 1.0000, 'REJECT', 300, 1, NOW(3), NOW(3));  -- 涨跌停禁挂

-- 4. 因子元数据
INSERT INTO factor_meta (factor_id, factor_name, category, formula, layer, created_at) VALUES
('momentum_20d', '20日动量',   'momentum', 'close/close_20d-1',       1, NOW(3)),
('rev_5d',       '5日反转',    'momentum', 'close_5d/close-1',        1, NOW(3)),
('vol_20d',      '20日波动率', 'momentum', 'std(returns,20)',         1, NOW(3)),
('pb',           '市净率',     'value',    'price/book',              1, NOW(3)),
('roe',          '净资产收益率','quality', 'net_profit/equity',       1, NOW(3)),
('growth_yoy',   '营收增速',   'growth',   'rev_yoy',                 1, NOW(3)),
('ai_residual',  'AI残差特征', 'ai',       'nn_residual(embedding)',  3, NOW(3));

-- 5. 资金账户
INSERT INTO capital_account (account_id, available, frozen, total, nav, updated_at) VALUES
('ACC01', 100000000.0000, 0.0000, 100000000.0000, 1.0000, NOW(3));

-- 6. 示例持仓
INSERT INTO position_snapshot (account_id, symbol, biz_date, qty, frozen_qty, avg_cost, market_value, pnl, updated_at) VALUES
('ACC01', '600519', CURDATE(), 10000, 0, 1680.0000, 18000000.0000, 1200000.0000, NOW(3)),
('ACC01', '300750', CURDATE(), 50000, 0,  210.0000, 12000000.0000,  300000.0000, NOW(3));
