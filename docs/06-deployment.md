# 06 · 部署方案与三环境隔离

> 对标私募工业标准：K8s 弹性调度、开盘峰值扩容、回测 / 仿真 / 实盘严格隔离、主备容灾。

---

## 1. 整体拓扑

```
                         ┌──────────── 行情厂商 / 券商柜台 ────────────┐
                         │  (Ctp/XTP/奇点 FIX / 原生 API)              │
                         └───────────────────┬─────────────────────────┘
                                             │ 专线 / 低延迟网络
        ┌────────────────────────────────────▼────────────────────────────────┐
        │                         K8s 集群（交易域）                              │
        │  ┌────────┐ ┌────────┐ ┌────────┐ ┌────────┐ ┌────────┐ ┌────────┐   │
        │  │ data   │ │strategy│ │ risk   │ │exec    │ │ledger  │ │monitor │   │
        │  │ (Flink)│ │(Go/Py) │ │(Go)    │ │(Go)    │ │(Go)    │ │(Grafana│  │
        │  └───┬────┘ └───┬────┘ └───┬────┘ └───┬────┘ └───┬────┘ │+Kafka) │  │
        │      └──────────┴─────────┴─────────┴─────────┘      └────────┘   │
        └───────────────────────────────┬────────────────────────────────────┘
                                        │
              ┌─────────────┬────────────┴─────────────┬──────────────┐
              ▼             ▼                          ▼              ▼
         Kafka 集群    Redis(Cluster)            MySQL(主从)      ClickHouse
         (事件总线)    (热数据/因子)             (强一致台账)     (时序行情)
```

---

## 2. 中间件部署

| 组件 | 部署形态 | 关键配置 |
|------|----------|----------|
| Kafka | 3 broker 集群 | 分区按 symbol 哈希；`acks=all`；保留 7d（可回放） |
| Redis | Cluster / Sentinel | 交易 Key 独立实例；关闭/轻量 AOF；内存 32G+ |
| MySQL | 主从 + 半同步 | 主写从读；每日全量 + binlog；TiDB 可选 |
| ClickHouse | 单/集群 | 时序行情、回测数据源；TTL 自动归档 |
| K8s | 多可用区 | 开盘 HPA 扩容策略；关键服务 PDB 保护 |

---

## 3. K8s 弹性调度（核心省钱 + 低延迟）

```yaml
# strategy 引擎：开盘扩容、盘后缩容
apiVersion: autoscaling/v2
kind: HorizontalPodAutoscaler
metadata: { name: quant-strategy }
spec:
  scaleTargetRef: { kind: Deployment, name: quant-strategy }
  minReplicas: 2          # 盘中 9:30 提升到 8
  maxReplicas: 12
  metrics:
    - type: Pods
      pods: { metric: { name: tick_lag }, target: { type: AverageValue, averageValue: "5ms" } }
```

- **CronHPA**：盘前 9:00 扩到峰值，收盘 15:10 缩到最小值，节省 60%+ 算力成本。
- **拓扑感知**：data/strategy/exec 调度到同可用区同节点池，降低网络 RTT。
- **资源隔离**：交易核心 Pod `Guaranteed` QoS，独占 CPU（CPU Manager + 绑核）。

---

## 4. 三环境隔离（回测 / 仿真 / 实盘）

| 环境 | 数据 | Redis 前缀 | 柜台 | 目的 |
|------|------|-----------|------|------|
| 回测 backtest | 历史重放 | `quant_bt:` | 无（模拟撮合） | 因子验证、参数寻优 |
| 仿真 paper | 实时镜像 | `quant_pa:` | 仿真柜台 | 策略验证、滑点/冲击建模 |
| 实盘 live | 真实 | `quant:` | 真实券商 | 资金运作 |

- 三环境 **共享同一套策略代码与因子定义**，仅通过配置切换数据源 / 前缀 / 柜台。
- 新策略发布链路：`回测达标 → 仿真 1~3 日 → 灰度(gray) 10% → 全量(on)`。
- 任何环境异常 → 实盘可一键 `halt`，不影响研究与仿真。

---

## 5. 可用性与容灾

| 风险 | 方案 |
|------|------|
| 单点故障 | 所有无状态服务多副本；MySQL 主从；Redis Cluster |
| 柜台故障 | 主备柜台（如 XTP 主 + 奇点备），自动切换 |
| 网络分区 | 交易域独立 VPC + 专线；断连期间订单 PENDING，重连补单 |
| 数据丢失 | MySQL binlog + 每日备份；Redis 丢失由 Kafka 重放 + MySQL 校准 |
| 人为误操作 | 策略变更走 CI/CD + 审批；生产参数只读配置中心 |

---

## 6. 监控与可观测性

- **Metrics**：Prometheus + Grafana（延迟、回撤、夏普、拦截率、换手）
- **Logs**：Kafka 全链路事件 → Elasticsearch，支持按 order_id 回溯
- **Tracing**：下单全链路 TraceID，定位延迟瓶颈（行情→信号→风控→柜台）
- **告警**：回撤 / 延迟 / 拦截率突增 → 企业微信 / 电话
