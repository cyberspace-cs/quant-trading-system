# quant-trading-system

> 工业级中高频量化交易系统 · 对标 **九坤 / 明汯 / 幻方** 私募标准
>
> 覆盖：业务需求 → 五层架构 → 技术栈 → DB + Redis 详细设计 → 全链路时序 → 矩阵式风控 → 部署方案 → 面试口述。
> 可直接用于 **后端面试、项目复盘、简历落地**。

[![Go](https://img.shields.io/badge/Go-1.21-00ADD8?logo=go)](https://go.dev)
[![Python](https://img.shields.io/badge/Python-3.11-3776AB?logo=python)](https://python.org)
[![MySQL](https://img.shields.io/badge/MySQL-8.0-4479A1?logo=mysql)](https://mysql.com)
[![Redis](https://img.shields.io/badge/Redis-7.0-DC382D?logo=redis)](https://redis.io)
[![Kafka](https://img.shields.io/badge/Kafka-3.x-231F20?logo=apachekafka)](https://kafka.apache.org)

---

## 一、系统一句话定位

面向 **A 股中高频 / 中频多因子 / 日内 T+0 / 套利 / AI 量化** 的全链路交易系统：数据工业化、因子工厂、AI 模型迭代、超低延迟执行、多层矩阵风控、**离线回测 → 仿真 → 实盘三环境隔离**。

> 九坤核心特点：数据工业化、因子工厂、AI 模型迭代、超低延迟执行、多层矩阵风控、三环境隔离。

---

## 二、五层架构总览

```
┌──────────────────────────────────────────────────────────────────────┐
│                          监控告警 / 归因 / 运维调度                      │
│               （延迟监控 · 夏普 · 最大回撤 · 因子收益拆解）               │
└──────────────────────────────────────────────────────────────────────┘
        ▲                ▲                 ▲                ▲
        │ 行情事件        │ 信号事件         │ 下单事件        │ 成交回执事件
        │ 风控日志        │                 │                │
┌───────┴────┐   ┌───────┴────┐   ┌────────┴─────┐   ┌───────┴─────┐   ┌──────────┐
│ 数据 中台层 │──▶│ 策略 引擎层 │──▶│  风控 引擎层  │──▶│ 交易 执行层  │──▶│ 资产 台账层 │
│ (Factor    │   │ (Signal    │   │ (Gate +     │   │ (Algo +     │   │ (Ledger + │
│  Factory)  │   │  Engine)   │   │  Monitor)   │   │  OMS)       │   │  PnL)     │
└────────────┘   └────────────┘   └──────────────┘   └──────────────┘   └──────────┘
   Kafka+Flink     Python+Go        Redis 预载规则      TWAP/VWAP        MySQL 强一致
   Redis 因子池    PyTorch/XGB      毫秒级拦截         订单状态机         盘后三账合一
   ClickHouse 时序 策略热更新        违规订单丢弃        柜台对接          审计可追溯
```

**核心通信范式**：`事件驱动 EDA + Kafka 事件总线 + Redis 低延迟缓存 + MySQL 强一致台账`。

---

## 三、目录结构

```
quant-trading-system/
├── README.md                  # 本文件：系统总览
├── docs/                      # 设计文档（面试 / 复盘核心）
│   ├── 01-architecture.md     # 五层架构与逐层详细设计
│   ├── 02-redis-design.md     # Redis 键设计 / 数据结构 / 最佳实践
│   ├── 03-mysql-design.md     # MySQL 表结构 + 完整 DDL 说明
│   ├── 04-sequence.md         # 全链路时序（盘前→盘中→盘后）
│   ├── 05-risk-system.md      # 矩阵式三层风控体系
│   ├── 06-deployment.md       # K8s 部署 / 三环境隔离 / 容灾
│   └── 07-interview.md        # 1 分钟口述 + 高端追问
├── sql/
│   ├── 01_schema.sql          # MySQL 工业级建表（持仓/订单/成交/资金/风控/绩效）
│   └── 02_init_data.sql       # 初始化配置数据
├── go/                        # 实盘引擎（Go，低延迟）
│   ├── cmd/main.go            # CLI 编排：装配五层 + 端到端演示
│   ├── cmd/server/main.go     # 工作台服务：仿真循环 + HTTP/SSE + 静态托管
│   └── internal/
│       ├── common/            # 领域模型：Tick/Order/Signal/Position/PnL
│       ├── data/              # 数据中台：行情订阅 + 因子写入 Redis
│       ├── strategy/          # 策略引擎：多因子打分 + 信号聚合
│       ├── risk/              # 风控引擎：事前/事中/事后
│       ├── execution/         # 交易执行：拆单 + 订单状态机 + OMS
│       ├── ledger/            # 资产台账：MySQL 落地 + 盘后对账
│       └── api/               # HTTP/SSE 接口层：overview/positions/orders/risk…
├── web/                       # 交易工作台前端（零依赖，原生 JS + Canvas）
│   ├── index.html
│   ├── style.css              # 九坤风格：近白底 / 超大衬线数字 / 低饱和撞色
│   └── app.js                 # REST 拉取 + SSE 实时 + Canvas 图表
└── python/                    # 研究平台（Python，因子/回测）
    ├── factors/               # 因子工厂：量价/基本面/AI 因子
    ├── strategies/            # 多因子选股 / 日内 / 套利
    ├── risk/                  # 风控规则（研究侧）
    └── backtest/              # 回测框架 + 绩效归因
```

---

## 四、核心非功能指标（私募硬标准）

| 指标 | 目标 | 实现手段 |
|------|------|----------|
| 行情延迟 | 微秒 ~ 毫秒 | C++/Go 直连行情，Redis 缓存盘口 |
| 信号生成 | 单策略毫秒级 | Go 实盘加速 + Redis 因子池热读 |
| 下单延迟 | 百微秒级 | 内存订单簿 + FIX/原生 API |
| 系统可用性 | 交易日 99.99% | K8s 多副本 + 主备柜台 |
| 风控生效 | 事前强拦截 / 零违规 | 规则预载 Redis，违规丢弃 |
| 数据一致性 | 行情/持仓/订单/资金强一致 | 柜台回执为真值，盘后三账合一 |
| 迭代效率 | 回测→实盘 1 天内 | 三环境共享代码，灰度 + 熔断 |

---

## 五、业务约束（A 股合规）

- 日内不能裸做空、涨跌停、T+1 → 适配 **合规拆单** 与 **黑白名单**。
- 单票仓位、行业集中度、单日最大亏损、回撤阈值 → **强约束**。
- 高频禁止大单冲击 → **智能分片 + 时间切片**。
- 所有策略 → **可灰度、可熔断、可一键关停**。

---

## 六、快速开始

```bash
# 1) Go 实盘引擎（编译校验）
cd go && go mod tidy && go build ./... && go run ./cmd

# 2) Python 研究平台（因子 + 回测）
cd python && pip install -r requirements.txt
python -m backtest.run --strategy multi_factor --start 20240101 --end 20240630

# 3) 建库
mysql -uroot -p quant < sql/01_schema.sql
mysql -uroot -p quant < sql/02_init_data.sql
```

---

## 七、Web 交易工作台（九坤风格）

设计语言借鉴九坤官网：**近白暖底 + 超大衬线数字 + 低饱和撞色 + 细线大留白**，
内容换成交易运维：总资产、净值曲线、行业暴露、策略开关、持仓/订单/风控。

![工作台总览](docs/web_dashboard.png)

```bash
cd go && go run ./cmd/server -addr :8080 -web ../web
# 打开 http://localhost:8080
```

- 后端：`internal/api` 提供 19 个 REST/SSE 接口（overview/positions/orders/fills/signals/risk/strategies/reconcile/stream…）
- 前端：`web/` 零依赖原生 JS，SSE 每秒推送总览，Canvas 自绘净值曲线与迷你走势
- 详见 [docs/08-frontend-api.md](docs/08-frontend-api.md)：九坤官网拆解 + 前端设计 + **后端接口缺口清单**

---

## 八、与普通业务系统的本质区别（面试绝杀）

| 系统 | 一致性 | 核心矛盾 | 延迟诉求 |
|------|--------|----------|----------|
| IM | 最终一致 | 消息扩散 | 中 |
| 电商 | 强一致 | 库存防超卖 | 中 |
| 量化 | **盘中允许毫秒级不一致，盘后强对账** | 冲击成本 / 滑点 / 对抗市场 | **极致低延迟优先** |

> 量化系统：**低延迟优先于一致性**（盘中），**一致性兜底于台账**（盘后）。多层风控**不可逆拦截**，离线→仿真→实盘**严格隔离**。

---

## 九、License

MIT · 仅用于学习与面试复盘，实盘须接入合规柜台与风控。
