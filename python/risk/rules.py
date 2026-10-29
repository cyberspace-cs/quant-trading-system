"""研究侧风控规则：回测 / 仿真前对目标组合做合规校验。

与实盘（Go risk 包）规则口径一致，保证"回测通过的组合，实盘同样合法"。
"""
from __future__ import annotations

import pandas as pd


class RiskConfig:
    def __init__(self, max_position: float = 0.08, max_industry: float = 0.30,
                 max_drawdown: float = -0.10, max_daily_loss: float = -0.02) -> None:
        self.max_position = max_position
        self.max_industry = max_industry
        self.max_drawdown = max_drawdown
        self.max_daily_loss = max_daily_loss


def check_weights(weights: pd.Series, industry: pd.Series, cfg: RiskConfig) -> list[str]:
    """校验目标权重：单票仓位 / 行业集中度。返回违规描述列表。"""
    violations: list[str] = []
    for sym, w in weights.items():
        if abs(w) > cfg.max_position:
            violations.append(f"单票 {sym} 仓位 {w:.2%} 超限 {cfg.max_position:.2%}")

    # 行业暴露
    df = pd.DataFrame({"w": weights, "ind": industry.reindex(weights.index)})
    ind_exp = df.groupby("ind")["w"].sum()
    for ind, w in ind_exp.items():
        if abs(w) > cfg.max_industry:
            violations.append(f"行业 {ind} 暴露 {w:.2%} 超限 {cfg.max_industry:.2%}")
    return violations


def check_drawdown(nav: pd.Series, cfg: RiskConfig) -> bool:
    """回撤熔断判定：当前回撤是否击穿阈值。"""
    peak = nav.cummax()
    dd = (nav - peak) / peak
    return bool(dd.min() < cfg.max_drawdown)


def check_daily_loss(daily_ret: float, cfg: RiskConfig) -> bool:
    """单日亏损熔断判定。"""
    return daily_ret < cfg.max_daily_loss
