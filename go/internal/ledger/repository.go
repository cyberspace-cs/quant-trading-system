package ledger

import (
	"context"
	"database/sql"
	"fmt"

	"quant/internal/common"
)

// MySQLRepository 生产台账实现（database/sql + MySQL/TiDB）。
// 建表见 sql/01_schema.sql；此处给出核心写入 SQL，可直接对接 go-sql-driver/mysql。
//
// 使用方式（生产）：
//
//	db, _ := sql.Open("mysql", dsn)
//	repo := ledger.NewMySQLRepository(db)
//	repo.SaveOrder(order) ...
type MySQLRepository struct {
	db *sql.DB
}

func NewMySQLRepository(db *sql.DB) *MySQLRepository { return &MySQLRepository{db: db} }

const (
	// 订单主表：只追加/状态由 order_id 幂等 upsert
	sqlUpsertOrder = `INSERT INTO order_main
		(order_id, account_id, strategy_id, symbol, side, order_type, orig_qty, filled_qty, price,
		 status, reject_reason, parent_id, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,FROM_UNIXTIME(?/1000),FROM_UNIXTIME(?/1000))
		ON DUPLICATE KEY UPDATE filled_qty=VALUES(filled_qty), status=VALUES(status),
			reject_reason=VALUES(reject_reason), updated_at=VALUES(updated_at)`

	// 成交明细：不可变，fill_id 幂等
	sqlInsertFill = `INSERT IGNORE INTO fill_detail
		(fill_id, order_id, symbol, side, filled_qty, fill_price, fill_amount, fill_time)
		VALUES (?,?,?,?,?,?,?,FROM_UNIXTIME(?/1000))`

	// 持仓快照：按 (account,symbol,biz_date) 幂等
	sqlUpsertPosition = `INSERT INTO position_snapshot
		(account_id, symbol, biz_date, qty, frozen_qty, avg_cost, market_value, pnl, updated_at)
		VALUES (?,?,CURDATE(),?,?,?,?,?,NOW(3))
		ON DUPLICATE KEY UPDATE qty=VALUES(qty), frozen_qty=VALUES(frozen_qty),
			avg_cost=VALUES(avg_cost), market_value=VALUES(market_value), pnl=VALUES(pnl), updated_at=NOW(3)`

	// 资金账户
	sqlUpsertCapital = `INSERT INTO capital_account
		(account_id, available, frozen, total, nav, updated_at)
		VALUES (?,?,?,?,?,NOW(3))
		ON DUPLICATE KEY UPDATE available=VALUES(available), frozen=VALUES(frozen),
			total=VALUES(total), nav=VALUES(nav), updated_at=NOW(3)`

	// 风控日志：只追加
	sqlInsertRiskLog = `INSERT INTO risk_log
		(strategy_id, rule_id, dimension, action, reason, created_at)
		VALUES (?,?,?,?,?,NOW(3))`
)

func (r *MySQLRepository) SaveOrder(o *common.Order) {
	_, _ = r.db.ExecContext(context.Background(), sqlUpsertOrder,
		o.OrderID, o.AccountID, o.StrategyID, o.Symbol, int(o.Side), o.OrderType,
		o.OrigQty, o.FilledQty, o.Price, o.Status, o.RejectRsn, o.ParentID, o.CreatedAt, o.UpdatedAt)
}

func (r *MySQLRepository) SaveFill(f *common.Fill) {
	_, _ = r.db.ExecContext(context.Background(), sqlInsertFill,
		f.FillID, f.OrderID, f.Symbol, int(f.Side), f.FilledQty, f.FillPrice, f.FillAmount, f.FillTime)
}

func (r *MySQLRepository) SavePosition(accountID string, p *common.Position) {
	_, _ = r.db.ExecContext(context.Background(), sqlUpsertPosition,
		accountID, p.Symbol, p.Qty, p.FrozenQty, p.AvgCost, p.MarketValue, p.PnL)
}

func (r *MySQLRepository) SaveCapital(c common.Capital) {
	_, _ = r.db.ExecContext(context.Background(), sqlUpsertCapital,
		c.AccountID, c.Available, c.Frozen, c.Total, c.Nav)
}

func (r *MySQLRepository) SaveRiskLog(strategyID, ruleID, dimension, action, reason string) {
	_, _ = r.db.ExecContext(context.Background(), sqlInsertRiskLog,
		strategyID, ruleID, dimension, action, reason)
}

// QueryPositions 盘后对账读取（真值）
func (r *MySQLRepository) QueryPositions(accountID string) (map[string]int64, error) {
	rows, err := r.db.QueryContext(context.Background(),
		`SELECT symbol, qty FROM position_snapshot WHERE account_id=? AND biz_date=CURDATE()`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var s string
		var q int64
		if err := rows.Scan(&s, &q); err != nil {
			return nil, err
		}
		out[s] = q
	}
	return out, rows.Err()
}

var _ = fmt.Sprintf // keep fmt import for future logging
