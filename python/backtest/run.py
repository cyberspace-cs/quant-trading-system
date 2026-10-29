"""回测入口：python -m backtest.run --strategy multi_factor

演示多因子选股全流程：合成行情 -> 因子 -> 打分 -> 调仓 -> 回测 -> 绩效。
"""
from __future__ import annotations

import argparse

import pandas as pd

from backtest.engine import generate_synthetic_data, run_backtest
from strategies.multi_factor import MultiFactorStrategy


def main() -> None:
    ap = argparse.ArgumentParser(description="quant-trading-system backtest")
    ap.add_argument("--strategy", default="multi_factor")
    ap.add_argument("--start", default="")
    ap.add_argument("--end", default="")
    ap.add_argument("--data", default="", help="CSV 路径（index=日期, columns=收盘价）；缺省用合成数据")
    ap.add_argument("--top-n", type=int, default=8)
    ap.add_argument("--rebalance", type=int, default=5, help="调仓周期（交易日）")
    ap.add_argument("--cost", type=float, default=0.0015, help="单边交易成本")
    args = ap.parse_args()

    if args.data:
        close = pd.read_csv(args.data, index_col=0, parse_dates=True)
        volume = close * 0 + 1_000_000  # 无成交量时退化
        if args.start:
            close = close.loc[args.start:]
            volume = volume.loc[args.start:]
        if args.end:
            close = close.loc[: args.end]
            volume = volume.loc[: args.end]
    else:
        close, volume = generate_synthetic_data(n_days=250, n_symbols=30)

    strategy = MultiFactorStrategy(top_n=args.top_n, lookback=20)
    print(f"回测标的={close.shape[1]} 交易日={close.shape[0]} 策略={args.strategy} "
          f"TopN={args.top_n} 调仓={args.rebalance}日 成本={args.cost:.3%}")

    result = run_backtest(close, volume, strategy, rebalance_days=args.rebalance, cost=args.cost)
    m = result["metrics"]

    print("\n================ 绩效指标 ================")
    rows = [
        ("累计收益", f"{m['cum_return']:.2%}"),
        ("年化收益", f"{m['annual_return']:.2%}"),
        ("夏普比率", f"{m['sharpe']:.2f}"),
        ("最大回撤", f"{m['max_drawdown']:.2%}"),
        ("胜率", f"{m['win_rate']:.1%}"),
        ("盈亏比", f"{m['profit_factor']:.2f}"),
        ("卡玛比率", f"{m['calmar']:.2f}"),
        ("日均换手", f"{result['turnover'].mean():.2%}"),
    ]
    for k, v in rows:
        print(f"  {k:<8}: {v}")

    # 基准：等权持有
    bench = close.pct_change().mean(axis=1).fillna(0)
    bench_nav = (1 + bench).cumprod()
    print(f"\n  基准等权累计收益: {bench_nav.iloc[-1] - 1:.2%}")
    print(f"  超额收益: {(result['nav'].iloc[-1] - bench_nav.iloc[-1]):.2%}")

    try:
        import matplotlib
        matplotlib.use("Agg")
        import matplotlib.pyplot as plt

        plt.figure(figsize=(10, 5))
        plt.plot(result["nav"].index, result["nav"].values, label="strategy")
        plt.plot(bench_nav.index, bench_nav.values, label="benchmark", alpha=0.7)
        plt.title("Multi-Factor Strategy NAV")
        plt.legend()
        plt.tight_layout()
        out = "backtest_nav.png"
        plt.savefig(out, dpi=120)
        print(f"\n净值曲线已保存: {out}")
    except Exception as e:  # 图表非必需
        print(f"(跳过绘图: {e})")


if __name__ == "__main__":
    main()
