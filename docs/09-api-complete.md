# 09 · 接口全量落地：从 19 个到 65 个

> 上一篇 `08-frontend-api.md` 列了一张「还缺什么」的清单（P0/P1/P2 共 15 类）。
> 这一篇是**把它们全部做完之后**的结果记录：65 个接口、5 角色 RBAC、幂等、限流、审计、双人复核、WebSocket、OpenAPI。
>
> 目标不是「接口数量好看」，而是**每个接口都能在私募真实运维里找到对应场景**，
> 并且能被前端、被 CI、被审计、被风控真正用起来。

---

## 一、总览

| 项 | 改造前 | 改造后 |
|----|--------|--------|
| HTTP 接口 | 19 | **65**（GET 39 / POST 19 / PUT 4 / DELETE 3） |
| 鉴权 | 无 | JWT(HS256) + RBAC 5 角色 9 权限 |
| 写接口保护 | 无 | 鉴权 + 幂等键 + 限流 + 审计四件套 |
| 审批流 | 无 | 风控规则变更 / 策略参数 / 出入金 三类双人复核 |
| 契约 | 无 | `/api/v1/openapi.json` + `/api/v1/docs` |
| 实时 | SSE 单频道 | SSE + **手写 WebSocket 多频道** |
| 前端 | 8 个只读视图 | 14 个视图，含 7 类写操作 |

代码分布（`go/internal/api/`，共 19 个文件）：

```
server.go        路由表 + 中间件链（request_id → CORS → 鉴权 → RBAC → 限流 → 幂等 → 审计）
httpx.go         统一响应 envelope / 错误码 / 分页排序过滤 / 泛型 Slice
jwt.go           手写 HS256 签名校验（RFC7519 最小实现）
auth.go          用户表 / 登录 / 刷新 / 登出黑名单 / RBAC 矩阵
ratelimit.go     令牌桶限流 + 幂等键存储
audit.go         写操作审计（谁/何时/何地/做了什么/结果/耗时）
store.go         可写域对象：参数版本、风控规则、因子库、划转、任务、回测
handlers_*.go    6 个业务域 handler
ws.go            RFC6455 握手 + 帧编解码 + 多频道订阅
openapi.go       从路由表生成 OpenAPI 3.0 + 自包含文档页
```

---

## 二、统一契约

### 1. 响应体

所有 `/api/v1` 接口（SSE / WS / metrics 除外）返回同一结构：

```json
{
  "code": 0,
  "msg": "ok",
  "data": { },
  "meta": { "page": 1, "size": 50, "total": 128, "pages": 3 },
  "request_id": "req-54265-47",
  "ts": 1793254265778
}
```

- `code=0` 成功，非 0 为业务错误码，同时映射 HTTP 状态码；
- `request_id` 全链路透传（响应头 `X-Request-Id`），排查问题直接按 ID 捞日志；
- `meta` 只在分页接口出现。

### 2. 错误码

| code | HTTP | 含义 |
|------|------|------|
| 0 | 200 | 成功 |
| 1001 | 400 | 参数错误 |
| 1002 | 401 | 未认证 / 令牌过期 |
| 1003 | 403 | 权限不足 |
| 1004 | 404 | 资源不存在 |
| 1005 | 409 | 状态冲突（重复撤单、无待审批变更…） |
| 1006 | 429 | 触发限流 |
| **1007** | **422** | **事前风控拦截**（业务语义独立，前端可单独提示） |
| 1008 | 200 | 幂等重放 |
| 1500 | 500 | 服务内部错误 |

> 为什么风控拦截单独给 422 而不是 400：
> 前端要区分「参数填错」和「风控不让下单」——前者改表单，后者要提示风险原因并留痕。

### 3. 分页 / 排序 / 过滤

统一参数：`page`、`size`（上限 500）、`sort`、`order=asc|desc`、`q`（关键字）。
后端由 `ParsePage` + 泛型 `Slice[T]` 实现，前端只认 `meta.total/pages`。

```http
GET /api/v1/orders?page=2&size=20&status=PARTIAL&sort=qty&order=desc&q=600519
```

---

## 三、鉴权与 RBAC

### 1. 账号（演示环境）

| 账号 | 口令 | 角色 | 能做什么 |
|------|------|------|----------|
| `admin` | `Admin@123` | admin | 全权限 |
| `trader` | `Trader@123` | trader | 下单/改撤单、策略开关与参数、回测 |
| `risk` | `Risk@123` | risk | 风控规则变更、审批（含参数/资金复核） |
| `research` | `Research@123` | researcher | 因子注册维护、回测 |
| `viewer` | `Viewer@123` | viewer | 只读 |

### 2. 权限矩阵

| 权限 | admin | trader | risk | researcher | viewer |
|------|:----:|:------:|:----:|:----------:|:------:|
| `read` | ✓ | ✓ | ✓ | ✓ | ✓ |
| `order:write` | ✓ | ✓ | | | |
| `strategy:write` | ✓ | ✓ | | | |
| `backtest:write` | ✓ | ✓ | | ✓ | |
| `factor:write` | ✓ | | | ✓ | |
| `risk:write` | ✓ | | ✓ | | |
| `capital:write` | ✓ | | | | |
| `approve` | ✓ | | ✓ | | |
| `system:write` | ✓ | | | | |

### 3. 令牌

- `POST /auth/login` → `access_token`（45 分钟）+ `refresh_token`（7 天）
- `POST /auth/refresh` → **刷新即轮换**，旧 refresh 立刻进黑名单（防重放）
- `POST /auth/logout` → 当前 jti 进黑名单，不等过期即失效
- 签名：`base64url(header).base64url(claims).HMAC-SHA256`，常量时间比较防时序侧信道

> 为什么手写 JWT 而不用 `golang-jwt`：仓库坚持零外部依赖（`go run` 即跑），
> 且面试时能讲清 header/payload/signature 三段结构与篡改检测，比"我会调库"更有说服力。

---

## 四、治理四件套

### 1. 限流（令牌桶，读写分离）

- 读 80 rps（突发 160），写 8 rps（突发 16）；
- key 维度：登录用户 ID，未登录退化到 IP；
- 响应头 `X-RateLimit-Remaining`，超限返回 `Retry-After: 1`。

> 量化系统里限流不是防用户，是**防策略异常刷单把柜台打挂**。

### 2. 幂等（Idempotency-Key）

写接口带 `Idempotency-Key` 时，服务端缓存首次响应 24h；重复请求直接回放，
并返回 `X-Idempotent-Replay: true`。只缓存 2xx —— 失败请求允许客户端改参数重试。

### 3. 审计

所有写操作（含手动触发的对账/任务）落一条审计：
`用户 / 角色 / 动作 / 资源 / 方法 / 路径 / IP / 状态码 / 耗时 / request_id / 详情`。

`GET /audit/logs?user=&action=&q=` 可查，`GET /audit/stats` 给动作分布。

### 4. 双人复核

三类高风险变更必须「提交人 ≠ 审批人」：

| 场景 | 触发条件 |
|------|----------|
| 风控规则阈值 | 放宽幅度 > 20%，或把 REJECT 降级为 WARN |
| 策略参数 | 任一参数相对变动 > 30% |
| 出入金 | 一律需复核（申请人不能自审） |

---

## 五、接口全量清单（65）

### 系统 / 认证（8）

| 方法 | 路径 | 权限 | 说明 |
|------|------|------|------|
| GET | `/health` | public | 健康检查、运行时长、交易日状态 |
| GET | `/openapi.json` | public | OpenAPI 3.0 规范 |
| GET | `/docs` | public | 自包含接口文档页（不依赖 CDN） |
| GET | `/metrics` | public | Prometheus 文本指标（17 项） |
| POST | `/auth/login` | public | 登录签发双令牌 |
| POST | `/auth/refresh` | public | 刷新并轮换 |
| GET | `/auth/me` | read | 当前用户 + 权限清单 |
| POST | `/auth/logout` | read | 吊销当前令牌 |

### 账户（3）

| GET | `/overview` | 总资产/净值/盈亏/回撤/夏普/换手/曲线 |
| GET | `/positions` | 持仓（盯市市值、浮盈、行业） |
| GET | `/performance` | 绩效指标与净值曲线 |

### 交易（7）

| 方法 | 路径 | 权限 | 说明 |
|------|------|------|------|
| GET | `/orders` | read | 订单列表（分页/状态/标的/策略/关键字） |
| GET | `/orders/{id}` | read | 详情：子单 + 成交流水 + 是否可改 |
| **POST** | **`/orders`** | order:write | **手动下单：事前风控 → 拆单 → 柜台** |
| **PUT** | **`/orders/{id}`** | order:write | **改单（仅 SENT/PARTIAL）** |
| **DELETE** | **`/orders/{id}`** | order:write | **撤单** |
| **POST** | **`/orders/batch-cancel`** | order:write | **批量撤单（按 ID / 标的 / 策略）** |
| GET | `/fills` | read | 成交明细 |
| GET | `/signals` | read | 策略信号流水 |

### 风控（7）

| 方法 | 路径 | 权限 | 说明 |
|------|------|------|------|
| GET | `/risk/logs` | read | 拦截审计 |
| GET | `/risk/rules` | read | 规则与阈值（含待审批） |
| POST | `/risk/rules` | risk:write | 新增规则 |
| PUT | `/risk/rules/{id}` | risk:write | 改阈值 / 启停（大改转待审批） |
| DELETE | `/risk/rules/{id}` | risk:write | 停用（软删除，保留审计） |
| POST | `/risk/rules/{id}/approve` | approve | 复核通过 |
| GET | `/risk/limits` | read | **各维度额度占用与剩余（风控台核心视图）** |

### 策略（8）

| 方法 | 路径 | 权限 | 说明 |
|------|------|------|------|
| GET | `/strategies` | read | 列表 + 当前参数 + 版本号 |
| GET | `/strategies/{id}` | read | 详情 + 参数 + 版本 + 统计 |
| POST | `/strategies/{id}/switch` | strategy:write | on/off/gray/halt |
| PUT | `/strategies/{id}/params` | strategy:write | 参数热更新（大改转审批） |
| GET | `/strategies/{id}/versions` | read | 版本历史 |
| POST | `/strategies/{id}/versions/{ver}/approve` | approve | 审批通过并热更新引擎 |
| POST | `/strategies/{id}/versions/{ver}/reject` | approve | 驳回 |
| POST | `/strategies/{id}/backtest` | backtest:write | 快捷发起回测 |

### 因子（6）

| 方法 | 路径 | 权限 | 说明 |
|------|------|------|------|
| GET | `/factors?symbol=` | read | 单标的实时因子值（兼容旧接口） |
| GET | `/factors/meta` | read | 因子库（类别/IC/ICIR/t值/覆盖率） |
| POST | `/factors` | factor:write | 注册自定义因子（ID 白名单校验） |
| PUT | `/factors/{id}` | factor:write | 更新元数据 / 上下线 |
| DELETE | `/factors/{id}` | factor:write | 删除（内置因子禁删） |
| GET | `/factors/{id}/ic` | read | IC 序列 + 10 分层 + 显著性结论 |

### 行情（4）

| GET | `/market/tick` | 全池最新行情 |
| GET | `/market/kline` | K 线聚合（1s/5s/30s/1m/5m/15m，由逐笔聚合） |
| GET | `/market/depth` | 五档盘口 + 买卖价差 |
| GET | `/market/pool` | 股票池 / 黑名单 / 涨跌停 |

### 回测（3）

| GET | `/backtests` | 任务列表（不含曲线，省带宽） |
| POST | `/backtests` | **创建异步任务**，立即返回 job |
| GET | `/backtests/{id}` | 进度 + 结果（夏普/回撤/胜率/曲线/基准对比） |

### 报表（2）

| GET | `/reports/daily?date=&format=csv` | 盘后日报（含 CSV 导出，带 BOM 防 Excel 乱码） |
| GET | `/reports/attribution?dim=strategy\|industry\|symbol` | 收益归因三视角 |

### 对账（2）

| GET | `/reconcile` | 三账合一当前结果 |
| POST | `/reconcile/run` | 手动触发（写审计） |

### 资金（4）

| GET | `/capital` | 可用/冻结/总资产/净值/已实现 |
| GET | `/capital/transfers` | 划转流水 |
| POST | `/capital/transfers` | 发起出入金（待审批） |
| POST | `/capital/transfers/{id}/approve` | 审批（通过后真实调资金账户） |

### 审计（2）

| GET | `/audit/logs` | 分页 + 用户/动作/关键字过滤 |
| GET | `/audit/stats` | 动作分布 |

### 系统（6）

| GET | `/system/config` | 运行配置、特性开关、限流参数、中间件形态 |
| GET | `/system/users` | 用户与角色权限矩阵 |
| GET | `/system/tasks` | 定时任务与上次结果 |
| POST | `/system/tasks/{id}/run` | 手动触发（盘前初始化/对账/归档…） |
| POST | `/system/halt` | **一键全停：策略全部熔断 + 停止信号生成** |
| POST | `/system/resume` | 恢复交易 |

### 实时（2）

| GET | `/stream` | SSE 每秒推送 snapshot |
| GET | `/ws` | WebSocket 多频道 |

---

## 六、WebSocket 协议

握手：`GET /api/v1/ws?token=<jwt>&channels=overview,tick,risk`

> 浏览器的 `WebSocket` 构造器不能自定义 Header，所以 WS 的令牌走 query 参数（服务端同样校验签名与黑名单）。

服务端 → 客户端：

```json
{"type":"hello","server":"quant-ws","version":"2.0.0","ts":...}
{"type":"overview","data":{...},"ts":...}
{"type":"tick","data":[{...}],"ts":...}
{"type":"risk","data":[{...}],"ts":...}
```

客户端 → 服务端：

```json
{"op":"subscribe","channels":["tick","risk"]}
{"op":"unsubscribe","channels":["risk"]}
```

实现要点：手写 RFC6455 —— 握手用 `SHA1(key + GUID)` + Base64；
帧头支持 7/16/64 位长度；客户端帧必须解掩码；支持 ping/pong/close。

---

## 七、关键实现说明

### 1. 手动下单链路（`POST /orders`）

```
入参校验（整手、方向、算法）
  → 事前风控 Check（涨跌停/黑名单/单笔手数/单票仓位/行业暴露/日亏损/换手）
      ├─ 拒绝 → 写风控日志 → 422 code=1007（不进柜台）
      └─ 通过 → 生成母单（原子序号，防同毫秒撞号）
                → Slice 拆单（TWAP/VWAP/Adaptive）
                → 逐张子单 OMS.Submit（状态机 + 柜台 + 缓存 + 台账）
                → 聚合母单状态（全部成交=FILLED，部分=PARTIAL）
  → 审计落库（action=order.create）
```

### 2. 风控规则热更新要"真生效"

改完规则如果只写内存字典、不更新引擎阈值，就是**假风控**。
`applyRuleToEngine()` 在规则生效/复核通过后同步写回 `PreTradeEngine` 的 6 个阈值字段，
下一次下单立刻按新阈值判断。

### 3. 策略参数热更新要"真作用到策略"

`Server.OnParams` 回调由 `cmd/server` 装配：参数审批通过后，
`MultiFactorEngine.SetWeights()` 加写锁替换权重，打分时先快照再遍历（避免锁内调用外部方法死锁）。

### 4. 归因口径（与私募归因报告一致）

```
单标的 PnL = Σ卖出金额 - Σ买入金额 + 期末持仓市值      （现金流法）
策略 PnL  = 该策略所有订单对应成交的现金流汇总          （子单回退到母单归属）
行业 PnL  = 行业下所有标的 PnL 汇总
```

### 5. 回测可复现

种子由 `策略名 + 起止日期` 派生，同参数重复回测结果完全一致 —— 这是回测的底线，
否则参数调优就变成"碰运气"。

---

## 八、这次实现里发现并修掉的 5 个真实缺陷

写接口的过程逼出了几个藏在前端之下的 bug，都是"只看 CLI 输出发现不了"的：

| # | 缺陷 | 现象 | 根因 | 修复 |
|---|------|------|------|------|
| 1 | 风控总资产口径错误 | 正常工作台手动下单全被拦："单票仓位 714% 超限 8%" | 用 `可用资金 + 成本市值` 算总资产，建仓消耗现金后分母自我收缩，形成死亡螺旋 | 改用**盯市总资产**（`mtmTotal`） |
| 2 | 成交回执指针复用 | 订单详情里每笔成交出现两次，且数值都是最后一笔 | `for _, f := range fills { SaveFill(&f) }` —— Go 1.21 range 变量复用，所有指针指向同一块内存 | 取切片副本再传指针 |
| 3 | 订单号同毫秒撞号 | 并发下单时订单数量对不上，前一张被覆盖 | `ORD-HHMMSS-毫秒后四位` 在同一毫秒内重复 | 改用**原子自增序号** |
| 4 | 母单不落台账 | 订单列表「暂无数据」，但成交、持仓都正常 | 仿真循环只 `Submit` 子单，母单从未 `SaveOrder` | 母单也落库并聚合状态 |
| 5 | 幂等缓存了失败响应 | 首次 422 后客户端改参数重试，仍返回旧的 422 | 缓存条件写成 `status < 500` | 只缓存 2xx |

> 这 5 条建议直接写进面试的项目复盘里：**"接口一接，问题全现"** 比任何架构图都有说服力。

---

## 九、测试与验收

### 单元测试（`go test ./...`）

| 包 | 覆盖 |
|----|------|
| `internal/api` | JWT 签名/篡改/过期、RBAC 拒绝、下单生命周期、风控拦截、幂等、规则双人复核、参数版本审批、分页、限流、WS Accept、审计、OpenAPI 合法性 |
| `internal/execution` | 状态机合法/非法跃迁、拆单总量守恒 |
| `internal/risk` | 涨跌停/黑名单/超手数拦截、合法单放行 |
| `internal/ledger` | 三账合一差异检出 |

### 端到端冒烟（`python3 scripts/smoke_test.py`）

**61 项断言全部通过**，覆盖 14 个场景：健康检查、5 角色登录、31 个只读接口、
下单/幂等/改单/撤单、事前风控拦截、规则审批、参数版本、异步回测、因子 CRUD + IC、
报表 CSV、资金审批、系统任务与熔断、审计留痕、限流、WebSocket 握手。

```bash
cd go && go run ./cmd/server -addr :8080 -web ../web &
python3 scripts/smoke_test.py
#   通过 61 / 失败 0
```

---

## 十、还没做的（诚实清单）

| 项 | 说明 | 建议做法 |
|----|------|----------|
| 持久化 | 仍是内存台账，重启即失 | 接 `ledger.MySQLRepository`（SQL 已写好） |
| 缓存/消息 | `MemoryCache` / `MemoryBus` | go-redis Cluster / Kafka |
| 多租户 | 单账户 | 请求上下文带 tenant，Redis Key 加前缀 |
| gRPC | 只有 REST | 内部服务间用 gRPC + protobuf |
| 回测引擎 | 简化版（单标的组合近似） | 接真实撮合、逐笔成交、冲击成本模型 |
| 前端构建 | 原生 JS 单文件 | 上规模后迁 Vite + TS + 组件库 |

---

## 十一、一句话总结

> 从「只有 CLI」到「65 个接口 + 5 角色 RBAC + 幂等限流审计三件套 + 双人复核 + WebSocket」，
> 这中间不是堆接口，而是把**「谁能做什么、做了怎么留痕、出问题怎么追溯」**想清楚。
> 量化系统的接口层，本质是把交易纪律固化成代码。
