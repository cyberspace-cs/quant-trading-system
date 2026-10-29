"""向量化回测引擎 + 绩效归因。

支持：多因子选股回测、调仓成本、绩效指标（夏普/最大回撤/胜率/盈亏比/卡玛）。
"""
from __future__ import annotations

import numpy as np
import pandas as pd


def generate_synthetic_data(n_days: int = 250, n_symbols: int = 30, seed: int = 42):
    """生成合成行情（几何布朗运动），用于无数据源时的演示。"""
    rng = np.random.default_rng(seed)
    dates = pd.bdate_range(end=pd.Timestamp.today().normalize(), periods=n_days)
    symbols = [f"S{i:03d}" for i in range(n_symbols)]

    # 每个标的不同漂移与波动（漂移差异明显，便于动量因子捕捉趋势）
    mu = rng.normal(0.0006, 0.0012, n_symbols)
    sigma = rng.uniform(0.010, 0.020, n_symbols)
    rets = rng.normal(mu, sigma, size=(n_days, n_symbols))
    prices = 100 * np.exp(np.cumsum(rets, axis=0))
    close = pd.DataFrame(prices, index=dates, columns=symbols)
    volume = pd.DataFrame(
        rng.integers(1_000_000, 10_000_000, size=(n_days, n_symbols)), index=dates, columns=symbols
    )
    return close, volume


def _daily_weights(target: pd.DataFrame, rebalance_dates, all_dates) -> pd.DataFrame:
    """把调仓日目标权重前向填充为每日持仓权重。"""
    daily = pd.DataFrame(0.0, index=all_dates, columns=target.columns)
    cur = pd.Series(0.0, index=target.columns)
    reb = set(rebalance_dates)
    for dt in all_dates:
        if dt in reb:
            cur = target.loc[dt]
        daily.loc[dt] = cur
    return daily


def run_backtest(close: pd.DataFrame, volume: pd.DataFrame, strategy,
                 rebalance_days: int = 5, cost: float = 0.001) -> dict:
    """执行回测，返回净值曲线、绩效指标与每日权重。"""
    returns = close.pct_change().fillna(0.0)

    # 调仓日：从 lookback 之后每 rebalance_days 一次
    start = getattr(strategy, "lookback", 20)
    all_dates = close.index
    rebalance_dates = [all_dates[i] for i in range(start, len(all_dates), rebalance_days)]

    target = strategy.target_weights(close, volume)
    # 仅在调仓日采样
    target = target.loc[rebalance_dates]
    daily_w = _daily_weights(target, rebalance_dates, all_dates)

    # 组合日收益 = 前一日权重 × 当日收益
    port_ret = (daily_w.shift(1).fillna(0.0) * returns).sum(axis=1)

    # 换手成本
    turnover = (daily_w - daily_w.shift(1).fillna(0.0)).abs().sum(axis=1)
    port_ret = port_ret - turnover * cost

    nav = (1 + port_ret).cumprod()
    return {
        "nav": nav,
        "returns": port_ret,
        "weights": daily_w,
        "turnover": turnover,
        "metrics": performance(port_ret, nav),
    }


def performance(returns: pd.Series, nav: pd.Series) -> dict:
    """绩效指标。"""
    n = len(returns)
    if n == 0:
        return {}
    ann_ret = nav.iloc[-1] ** (252 / n) - 1 if nav.iloc[-1] > 0 else -1
    std = returns.std()
    sharpe = (returns.mean() / std * np.sqrt(252)) if std > 0 else 0.0

    peak = nav.cummax()
    dd = (nav - peak) / peak
    max_dd = dd.min()

    wins = (returns > 0).sum()
    losses = (returns < 0).sum()
    win_rate = wins / (wins + losses) if (wins + losses) > 0 else 0.0
    gain = returns[returns > 0].sum()
    loss = -returns[returns < 0].sum()
    profit_factor = gain / loss if loss > 0 else float("inf")

    calmar = ann_ret / abs(max_dd) if max_dd < 0 else 0.0
    return {
        "cum_return": nav.iloc[-1] - 1,
        "annual_return": ann_ret,
        "sharpe": sharpe,
        "max_drawdown": max_dd,
        "win_rate": win_rate,
        "profit_factor": profit_factor,
        "calmar": calmar,
    }
