# 08 · 九坤官网拆解 · 前端设计 · 后端接口缺口

> 目标：先看九坤官网「长什么样」，把它的视觉语言迁移到我们的交易工作台；
> 再对照现有后端，列出**缺哪些接口**，并把关键接口补齐、跑通前端。

---

## 一、九坤官网视觉拆解（ubiquant.com）

真实访问路径：首页先弹「合格投资者认证」，点击「接受」后进入。

### 1. 视觉语言

| 维度 | 观察 | 可复用点 |
|------|------|----------|
| 底色 | 近白暖灰（#f7f7f5 级别），几乎无重色块 | 大面积留白，靠排版建立高级感 |
| 文字 | 近黑（#111），正文灰阶克制 | 不用纯黑纯白，降低对比疲劳 |
| 数字 | **超大字号**：`2012年 / 800亿 / 20000+` | 数字即视觉主体，字重高、字距紧 |
| 强调色 | 低饱和撞色圆点：蜜桃橙、雾蓝、灰绿 | 小面积点缀，不做大面积填充 |
| 字体 | 标题偏衬线/宋体气质，正文无衬线 | 标题衬线 + 正文无衬线 的混排 |
| 布局 | 居中单列、超大行距、右侧细导航 | 细导航 + 内容居中的杂志式排版 |
| 装饰 | 半调网点、柔焦圆点、3D 图形 | 低存在感纹理，避免抢内容 |
| 动效 | 首屏 WebGL 图形 + 数据城市纹理 | 首屏用「数据感」建立行业属性 |
| 合规 | 合格投资者弹窗 + 风险提示常驻 | 金融站点必备的合规层 |

### 2. 信息结构

```
首屏 Hero（3D 图形 + 品牌主张）
  → 主张 + 三行副文案 + 「了解更多」胶囊按钮
  → 六宫格数据（成立年份 / 管理规模 / 客户数 / 奖项 / 学历 / 博士数）
  → 页脚（Logo + 站点地图 + 三个二维码 + ICP 备案）
右侧固定细导航：登录注册 / 关于九坤 / 招贤纳士 / 量化竞赛 / 社会责任 / 中英切换
```

### 3. 结论

九坤官网是**品牌向**站点，不是交易系统。我们不该照抄内容，而应迁移它的**设计语法**：
近白底、超大衬线数字、细线分隔、低饱和点缀、大留白、克制的动效。
把「品牌感」翻译成「机构级交易工作台的秩序感」。

---

## 二、我们的前端：交易工作台

代码：`web/index.html` + `web/style.css` + `web/app.js`（零依赖，原生 JS + Canvas）。

### 1. 设计转译

| 九坤元素 | 工作台落地 |
|----------|------------|
| 近白暖底 | `--bg:#f5f4f0`，卡片纯白 |
| 超大数字 | 总资产 `1.00 亿` 用 64px 衬线体 |
| 低饱和撞色 | 蜜桃/雾蓝/灰绿 用于行业条与图表 |
| 细导航 | 左侧 236px 细导航 + 编号 01–08 |
| 半调网点/柔焦圆点 | Hero 背景网点 + 三个模糊圆点 |
| 合规提示 | 侧栏底部常驻风险提示 |
| 右侧细导航 | 改为左侧（工作台操作习惯优先） |

### 2. 页面结构

```
侧栏：品牌 / 8 个视图（总览·持仓·订单·成交·信号·风控·策略·对账）/ 状态·延迟·合规
主区：
  Topbar    交易日 · 账户 · 盘中状态 · 刷新
  Hero      总资产（大字）+ 当日盈亏 + 净值 + 迷你净值曲线
  KPI       当日盈亏 / 最大回撤 / 夏普 / 换手率
  图表区    净值曲线（Canvas）+ 行业暴露 + 策略开关
  数据表    持仓/订单/成交/信号/风控（Tab 切换）
  对账      三账合一结果
```

### 3. 交互

- SSE 每秒推送 `overview`，资产/净值/曲线实时刷新；
- 表格每 5s 轮询，策略每 8s 刷新；
- 策略开关支持 `on / off / gray / halt`，点击即 `POST` 切换；
- Canvas 按 `devicePixelRatio` 适配高清屏；
- 涨红跌绿（A 股习惯）：盈利红、亏损绿。

---

## 三、后端接口：现状 → 缺口 → 已补齐

### 1. 改造前（最大问题）

> 仓库只有 **CLI**：`go run ./cmd` 打印一次结果。
> **没有任何 HTTP / WebSocket 接口，没有鉴权，没有查询能力，前端无从对接。**

### 2. 本次已补齐的接口（`internal/api` + `cmd/server`）

| # | 方法 | 路径 | 说明 |
|---|------|------|------|
| 1 | GET | `/api/v1/health` | 健康检查 |
| 2 | GET | `/api/v1/overview` | 总资产/净值/盈亏/回撤/夏普/换手/曲线 |
| 3 | GET | `/api/v1/positions` | 持仓（盯市市值/浮盈） |
| 4 | GET | `/api/v1/orders?limit=` | 订单流水 |
| 5 | GET | `/api/v1/fills?limit=` | 成交明细 |
| 6 | GET | `/api/v1/signals?limit=` | 策略信号 |
| 7 | GET | `/api/v1/risk/logs?limit=` | 风控拦截审计 |
| 8 | GET | `/api/v1/risk/rules` | 风控规则与阈值 |
| 9 | GET | `/api/v1/strategies` | 策略列表与运行态 |
| 10 | POST | `/api/v1/strategies/{id}/switch` | 策略开关/灰度/熔断 |
| 11 | GET | `/api/v1/performance` | 绩效归因（夏普/回撤/胜率/盈亏比/卡玛） |
| 12 | GET | `/api/v1/factors?symbol=` | 单标的实时因子 |
| 13 | GET | `/api/v1/market/tick` | 全池最新行情 |
| 14 | GET | `/api/v1/market/pool` | 股票池/黑名单/涨跌停 |
| 15 | GET | `/api/v1/reconcile` | 三账合一结果 |
| 16 | POST | `/api/v1/reconcile/run` | 触发对账 |
| 17 | POST | `/api/v1/orders/{id}/cancel` | 撤单 |
| 18 | GET | `/api/v1/stream` | **SSE 实时推送**（overview 每秒） |
| 19 | GET | `/api/v1/metrics` | Prometheus 文本指标 |

### 3. 仍然缺失、上生产必须补的接口（TODO）

| 域 | 缺失接口 | 优先级 | 说明 |
|----|----------|--------|------|
| 认证 | `POST /auth/login`、`POST /auth/refresh`、`GET /auth/me` | P0 | JWT + RBAC（研究员/交易员/风控/只读） |
| 订单 | `POST /orders` 手动下单、`GET /orders/{id}`、批量撤单、改单 | P0 | 幂等键 `Idempotency-Key` |
| 策略 | `PUT /strategies/{id}/params`、`POST /strategies/{id}/backtest`、版本管理 | P1 | 参数热更新需审批 + 审计 |
| 回测 | `POST /backtests`、`GET /backtests/{id}`、结果报告 | P1 | 异步任务 + 进度 |
| 报表 | `GET /reports/daily?date=`、`GET /reports/attribution` | P1 | 盘后归因、PDF/Excel 导出 |
| 因子 | `GET /factors/meta`、`GET /factors/{id}/ic`、因子库 CRUD | P1 | 因子生命周期 |
| 风控 | `POST/PUT/DELETE /risk/rules`、`GET /risk/limits` | P1 | 规则变更双人复核 |
| 资金 | `GET /capital`、`POST /capital/transfers` | P2 | 出入金审批流 |
| 行情 | `GET /market/kline`、`GET /market/depth`、`WS /ws` | P1 | 高频场景用 WebSocket 替代 SSE |
| 审计 | `GET /audit/logs` | P0 | 所有写操作留痕 |
| 系统 | `GET /system/config`、`GET /system/tasks` | P2 | 配置中心、定时任务 |
| 通用 | 分页 `page/size`、排序、过滤、OpenAPI/Swagger、限流 | P0 | 契约先行 |

### 4. 生产化改造点

1. **持久化**：当前 `Portfolio` 为内存实现，重启即失；接 `ledger.MySQLRepository`（已写好 SQL）落地。
2. **缓存**：`MemoryCache` 换 `go-redis`，键设计见 `docs/02-redis-design.md`。
3. **事件**：`MemoryBus` 换 Kafka，前端 SSE 改为消费 `order.fill / risk.alert`。
4. **实时**：SSE → WebSocket，支持订阅不同频道（tick/order/pnl）。
5. **鉴权**：JWT + RBAC；写接口幂等 + 审计 + 限流。
6. **多环境**：`quant_bt / quant_pa / quant` 前缀隔离，前端按环境切换。

---

## 四、运行

```bash
cd go
go run ./cmd/server -addr :8080 -web ../web
# 打开 http://localhost:8080
```

前端零依赖，直接由 Go 静态服务托管；SSE 断线自动重连。

---

## 五、一句话总结

> 九坤官网教我们的是**「用排版和留白建立机构感」**；
> 我们的工作台把它翻译成**「用超大数字 + 细线 + 低饱和点缀承载高密度交易数据」**。
> 而支撑这套前端的，是从 0 补出来的一层 HTTP/SSE 接口——这正是原仓库最大的缺口。
