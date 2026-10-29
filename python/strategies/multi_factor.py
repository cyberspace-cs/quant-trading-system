"""中频多因子选股策略（九坤主力策略原型）。

流程：量价因子 -> 截面 zscore -> 加权合成 -> 排序取 TopN -> 等权目标组合。
"""
from __future__ import annotations

import pandas as pd

from factors import factor_lib


class MultiFactorStrategy:
    def __init__(self, top_n: int = 10, lookback: int = 20,
                 weights: dict[str, float] | None = None) -> None:
        self.top_n = top_n
        self.lookback = lookback
        self.weights = weights or {
            "momentum": 1.0,
            "reversal": 0.3,
            "volatility": -0.5,   # 低波动优先
            "volume_ratio": 0.2,
        }

    def build_factors(self, close: pd.DataFrame, volume: pd.DataFrame) -> dict[str, pd.DataFrame]:
        """构造因子面板（index=date, columns=symbol）。"""
        cols = list(close.columns)
        return {
            "momentum": pd.DataFrame({s: factor_lib.momentum(close[s], self.lookback) for s in cols}),
            "reversal": pd.DataFrame({s: factor_lib.reversal(close[s], 5) for s in cols}),
            "volatility": pd.DataFrame({s: factor_lib.volatility(close[s], self.lookback) for s in cols}),
            "volume_ratio": pd.DataFrame({s: factor_lib.volume_ratio(volume[s], 5) for s in cols}),
        }

    def score(self, close: pd.DataFrame, volume: pd.DataFrame) -> pd.DataFrame:
        factors = self.build_factors(close, volume)
        return factor_lib.composite_score(factors, self.weights)

    def target_weights(self, close: pd.DataFrame, volume: pd.DataFrame) -> pd.DataFrame:
        """每个交易日给出目标权重（仅调仓日会被回测引擎采样）。"""
        score = self.score(close, volume)
        weights = pd.DataFrame(0.0, index=score.index, columns=score.columns)
        for dt in score.index:
            row = score.loc[dt].dropna()
            if len(row) < self.top_n:
                continue
            top = row.nlargest(self.top_n).index
            weights.loc[dt, top] = 1.0 / self.top_n
        return weights
