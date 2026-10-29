"""因子工厂：量价因子 / 截面标准化 / 中性化。

研究侧（Python）负责因子定义与挖掘，实盘侧（Go）读取中台算好的因子值。
所有因子函数输入为 pandas Series/DataFrame，输出同形状的因子值。
"""
from __future__ import annotations

import numpy as np
import pandas as pd


# ------------------------- 量价因子 -------------------------

def momentum(close: pd.Series, window: int = 20) -> pd.Series:
    """动量因子：过去 window 日收益率。"""
    return close.pct_change(window)


def reversal(close: pd.Series, window: int = 5) -> pd.Series:
    """反转因子：过去 window 日收益取负。"""
    return -close.pct_change(window)


def volatility(close: pd.Series, window: int = 20) -> pd.Series:
    """波动率因子：日收益滚动标准差。"""
    ret = close.pct_change()
    return ret.rolling(window).std()


def volume_ratio(volume: pd.Series, window: int = 5) -> pd.Series:
    """量比因子：当日成交量 / 过去 window 日均量。"""
    return volume / volume.rolling(window).mean()


# ------------------------- 截面处理 -------------------------

def zscore(df: pd.DataFrame) -> pd.DataFrame:
    """截面标准化：按日期对每个标的做 z-score。"""
    mean = df.mean(axis=1)
    std = df.std(axis=1).replace(0, np.nan)
    return df.sub(mean, axis=0).div(std, axis=0)


def industry_neutralize(factor: pd.DataFrame, industry: pd.Series) -> pd.DataFrame:
    """行业中性化：因子减去同行业均值。industry: index=symbol, value=industry_code。"""
    out = factor.copy()
    for dt in factor.index:
        row = factor.loc[dt]
        grp = row.groupby(industry).transform("mean")
        out.loc[dt] = row - grp
    return out


def rank_normalize(df: pd.DataFrame) -> pd.DataFrame:
    """截面百分位排名（0~1），抗极值。"""
    return df.rank(axis=1, pct=True)


# ------------------------- 因子合成 -------------------------

def composite_score(factors: dict[str, pd.DataFrame], weights: dict[str, float]) -> pd.DataFrame:
    """多因子合成：Σ w_i * zscore(factor_i)。"""
    total = None
    for name, w in weights.items():
        f = factors[name]
        z = zscore(f)
        total = z * w if total is None else total + z * w
    return total
