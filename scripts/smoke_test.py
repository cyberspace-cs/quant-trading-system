#!/usr/bin/env python3
"""
端到端接口冒烟测试（零依赖，仅标准库）。

用法：
    cd go && go run ./cmd/server -addr :8080 -web ../web &
    python3 scripts/smoke_test.py              # 默认 http://127.0.0.1:8080
    BASE=http://host:port python3 scripts/smoke_test.py

覆盖：鉴权/RBAC/限流/幂等/下单/改单/撤单/风控规则审批/策略参数版本/回测/
      因子库/报表 CSV/资金审批/审计/系统任务/熔断/WebSocket 握手与首帧。
"""
import base64
import hashlib
import json
import os
import socket
import ssl
import struct
import sys
import time
import urllib.error
import urllib.request

BASE = os.environ.get("BASE", "http://127.0.0.1:8080")
API = BASE + "/api/v1"

PASS = 0
FAIL = 0
FAILED = []


def check(name, cond, extra=""):
    global PASS, FAIL
    if cond:
        PASS += 1
        print(f"  \033[32m✓\033[0m {name}")
    else:
        FAIL += 1
        FAILED.append(name)
        print(f"  \033[31m✗\033[0m {name}  {extra}")


def req(method, path, body=None, token=None, headers=None, raw=False):
    url = API + path
    data = json.dumps(body).encode() if body is not None else None
    r = urllib.request.Request(url, data=data, method=method)
    r.add_header("Content-Type", "application/json")
    if token:
        r.add_header("Authorization", "Bearer " + token)
    for k, v in (headers or {}).items():
        r.add_header(k, v)
    try:
        with urllib.request.urlopen(r, timeout=15) as resp:
            payload = resp.read()
            if raw:
                return resp.status, payload, dict(resp.headers)
            try:
                return resp.status, json.loads(payload), dict(resp.headers)
            except Exception:
                return resp.status, payload, dict(resp.headers)
    except urllib.error.HTTPError as e:
        payload = e.read()
        try:
            return e.code, json.loads(payload), dict(e.headers)
        except Exception:
            return e.code, payload, dict(e.headers)


def login(user, pw):
    st, body, _ = req("POST", "/auth/login", {"username": user, "password": pw})
    assert st == 200 and body.get("code") == 0, f"login {user} failed: {st} {body}"
    return body["data"]


def section(title):
    print(f"\n\033[1m{title}\033[0m")


def main():
    section("0. 健康检查")
    st, b, _ = req("GET", "/health")
    check("GET /health 200", st == 200 and b["data"]["status"] == "ok", b)

    section("1. 认证与 RBAC")
    admin = login("admin", "Admin@123")
    trader = login("trader", "Trader@123")
    risk = login("risk", "Risk@123")
    viewer = login("viewer", "Viewer@123")
    check("5 角色登录成功", all([admin, trader, risk, viewer]))
    check("trader 权限含 order:write", "order:write" in trader["perms"])
    check("viewer 无 order:write", "order:write" not in viewer["perms"])

    st, b, _ = req("GET", "/overview")
    check("未带 token 访问 /overview 返回 401", st == 401, st)
    st, b, _ = req("GET", "/overview", token="bad.token.value")
    check("伪造 token 返回 401", st == 401, st)
    st, b, _ = req("POST", "/orders",
                   {"symbol": "600519", "side": "BUY", "qty": 100, "price": 1690},
                   token=viewer["access_token"])
    check("viewer 下单返回 403", st == 403, (st, b))

    st, b, _ = req("POST", "/auth/refresh", {"refresh_token": trader["refresh_token"]})
    check("refresh 换取新 access token", st == 200 and b["data"]["access_token"], st)
    st, b, _ = req("GET", "/auth/me", token=trader["access_token"])
    check("GET /auth/me 返回角色与权限", st == 200 and b["data"]["role"] == "trader", st)

    section("2. 只读接口全量")
    reads = [
        "/overview", "/positions", "/performance", "/orders?page=1&size=5",
        "/fills?size=5", "/signals?size=5", "/risk/logs?size=5", "/risk/rules",
        "/risk/limits", "/strategies", "/strategies/alpha_mf",
        "/strategies/alpha_mf/versions", "/factors/meta", "/factors?symbol=600519",
        "/factors/momentum_20d/ic", "/market/tick", "/market/kline?symbol=600519&period=1m&limit=30",
        "/market/depth?symbol=600519", "/market/pool", "/reconcile",
        "/reports/daily", "/reports/attribution?dim=strategy",
        "/backtests", "/capital", "/capital/transfers", "/audit/logs?size=5",
        "/audit/stats", "/system/config", "/system/users", "/system/tasks",
        "/openapi.json",
    ]
    bad = []
    for p in reads:
        st, b, _ = req("GET", p, token=trader["access_token"])
        if st != 200:
            bad.append((p, st))
    check(f"{len(reads)} 个只读接口全部 200", not bad, bad)

    st, b, _ = req("GET", "/orders?page=1&size=3", token=trader["access_token"])
    check("分页 meta 正确", st == 200 and b["meta"]["size"] == 3 and "total" in b["meta"], b.get("meta"))

    section("3. 手动下单 / 幂等 / 改单 / 撤单")
    idem = "smoke-" + str(int(time.time()))
    # 用低价标的 + 2000 股：既在风控额度内，又会被柜台部分成交（80%），
    # 从而能验证 PARTIAL 状态下的改单 / 撤单链路
    payload = {"symbol": "000001", "side": "BUY", "qty": 2000, "price": 12.0,
               "algo": "TWAP", "slices": 3, "strategy_id": "alpha_mf", "reason": "冒烟测试"}
    st, b, _ = req("POST", "/orders", payload, token=trader["access_token"],
                   headers={"Idempotency-Key": idem})
    ok = st == 200 and b["data"]["parent"]["order_id"]
    check("POST /orders 下单成功", st == 200, (st, b))
    oid = b["data"]["parent"]["order_id"] if st == 200 else ""
    nchild = len(b["data"]["children"]) if st == 200 else 0
    check("拆单生成 3 张子单", nchild == 3, nchild)
    check("子单数量之和 == 母单量", sum(c["qty"] for c in b["data"]["children"]) == 2000 if st == 200 else False)

    st2, b2, h2 = req("POST", "/orders", payload, token=trader["access_token"],
                      headers={"Idempotency-Key": idem})
    check("幂等键重放同一订单", st2 == 200 and b2["data"]["parent"]["order_id"] == oid, (st2, b2))
    check("重放响应头带 X-Idempotent-Replay", h2.get("X-Idempotent-Replay") == "true", h2.get("X-Idempotent-Replay"))

    st, b, _ = req("GET", f"/orders/{oid}", token=trader["access_token"])
    det = b.get("data") if isinstance(b, dict) and st == 200 else {}
    check("订单详情含子单与成交", st == 200 and len(det.get("children", [])) == 3, st)
    check("部分成交 -> 状态为 PARTIAL 且可改单",
          st == 200 and det.get("order", {}).get("status") == "PARTIAL" and det.get("amendable"),
          det.get("order", {}).get("status"))

    st, b, _ = req("PUT", f"/orders/{oid}", {"qty": 2000, "price": 12.2, "reason": "降低冲击"}, token=trader["access_token"])
    check("PUT 改单成功", st == 200, (st, b))

    st, b, _ = req("DELETE", f"/orders/{oid}", token=trader["access_token"])
    check("DELETE 撤单成功", st == 200, (st, b))
    st, b, _ = req("DELETE", f"/orders/{oid}", token=trader["access_token"])
    check("重复撤单返回 409", st == 409, st)

    st, b, _ = req("POST", "/orders/batch-cancel", {"symbol": "000001"}, token=trader["access_token"])
    check("批量撤单接口可用", st == 200 and "cancelled" in b["data"], st)

    section("4. 事前风控拦截")
    st, b, _ = req("POST", "/orders", {"symbol": "600519", "side": "BUY", "qty": 900000, "price": 1690}, token=trader["access_token"])
    check("超单笔手数被拦截 422", st == 422 and b["code"] == 1007, (st, b.get("msg")))
    st, b, _ = req("POST", "/orders", {"symbol": "ST002", "side": "BUY", "qty": 100, "price": 10}, token=trader["access_token"])
    check("黑名单标的被拦截", st == 422, (st, b.get("msg")))
    st, b, _ = req("POST", "/orders", {"symbol": "600519", "side": "BUY", "qty": 150, "price": 1690}, token=trader["access_token"])
    check("非整手委托被拒绝 400", st == 400, (st, b.get("msg")))

    section("5. 风控规则变更 + 双人复核")
    st, b, _ = req("PUT", "/risk/rules/R_POS", {"threshold": 0.085, "reason": "小幅收紧"}, token=risk["access_token"])
    check("小幅调整立即生效", st == 200 and b["data"].get("threshold") == 0.085, (st, b))
    st, b, _ = req("PUT", "/risk/rules/R_POS", {"threshold": 0.20, "reason": "大幅放宽"}, token=risk["access_token"])
    check("放宽 >20% 转待审批", st == 200 and b["data"].get("status") == "pending_approval", (st, b))
    st, b, _ = req("POST", "/risk/rules/R_POS/approve", None, token=risk["access_token"])
    check("提交人不能自审 403", st == 403, st)
    st, b, _ = req("POST", "/risk/rules/R_POS/approve", None, token=admin["access_token"])
    check("他人复核通过", st == 200 and b["data"]["threshold"] == 0.20, (st, b))

    section("6. 策略参数版本")
    st, b, _ = req("PUT", "/strategies/alpha_mf/params", {"params": {"rev_5d": 0.35}, "reason": "微调反转权重"}, token=trader["access_token"])
    check("小幅参数变更即时生效", st == 200 and b["data"]["status"] == "active", (st, b))
    ver_active = b["data"].get("version") if st == 200 else 0
    st, b, _ = req("PUT", "/strategies/alpha_mf/params", {"params": {"rev_5d": 1.9}, "reason": "大幅提高反转权重"}, token=trader["access_token"])
    check("变动 >30% 转待审批", st == 200 and b["data"]["status"] == "pending_approval", (st, b))
    ver_pending = b["data"].get("version") if st == 200 else 0
    st, b, _ = req("POST", f"/strategies/alpha_mf/versions/{ver_pending}/approve", None, token=trader["access_token"])
    check("参数提交人不能自审 403", st == 403, st)
    st, b, _ = req("POST", f"/strategies/alpha_mf/versions/{ver_pending}/approve", None, token=risk["access_token"])
    check("风控官复核通过参数", st == 200 and b["data"]["status"] == "active", (st, b))
    st, b, _ = req("GET", "/strategies/alpha_mf/versions", token=trader["access_token"])
    check("版本历史含 active/superseded", st == 200 and len(b["data"]) >= 3, st)

    st, b, _ = req("POST", "/strategies/alpha_mf/switch", {"status": "gray", "reason": "灰度"}, token=trader["access_token"])
    check("策略灰度切换", st == 200 and b["data"]["status"] == "gray", st)
    req("POST", "/strategies/alpha_mf/switch", {"status": "on"}, token=trader["access_token"])

    section("7. 异步回测")
    st, b, _ = req("POST", "/backtests", {"strategy": "alpha_mf", "start": "2023-01-01", "end": "2024-12-31"}, token=trader["access_token"])
    check("创建回测任务", st == 200 and b["data"]["status"] in ("queued", "running"), (st, b))
    bt = b["data"]["id"] if st == 200 else ""
    done = False
    for _ in range(60):
        time.sleep(0.3)
        st, b, _ = req("GET", f"/backtests/{bt}", token=trader["access_token"])
        if b["data"]["status"] == "done":
            done = True
            break
    res = b["data"].get("result") or {}
    check("回测异步完成", done, b["data"].get("status"))
    check("回测结果含夏普/回撤/曲线", done and res.get("sharpe") is not None and len(res.get("curve", [])) > 100, res.get("sharpe"))

    section("8. 因子库")
    st, b, _ = req("POST", "/factors", {"id": "smoke_turn", "name": "冒烟换手因子", "category": "量价",
                                        "formula": "mean(turn,20)", "direction": 1}, token=admin["access_token"])
    check("注册自定义因子", st == 200, (st, b))
    st, b, _ = req("PUT", "/factors/smoke_turn", {"status": "online"}, token=admin["access_token"])
    check("因子状态改为 online", st == 200 and b["data"]["status"] == "online", st)
    st, b, _ = req("GET", "/factors/smoke_turn/ic", token=trader["access_token"])
    check("IC 分析返回序列与分层", st == 200 and len(b["data"]["series"]) > 0 and len(b["data"]["deciles"]) == 10, st)
    st, b, _ = req("DELETE", "/factors/momentum_20d", None, token=admin["access_token"])
    check("内置因子禁删 409", st == 409, st)
    st, b, _ = req("DELETE", "/factors/smoke_turn", None, token=admin["access_token"])
    check("删除自定义因子", st == 200, st)
    st, b, _ = req("POST", "/factors", {"id": "BadID", "name": "x", "formula": "a"}, token=admin["access_token"])
    check("非法因子 ID 被拒 400", st == 400, st)

    section("9. 报表与 CSV 导出")
    st, b, _ = req("GET", "/reports/daily", token=trader["access_token"])
    check("日报含盈亏/成交/风控统计", st == 200 and "day_pnl" in b["data"] and "risk_by_rule" in b["data"], st)
    st, raw, hdrs = req("GET", "/reports/daily?format=csv", token=trader["access_token"], raw=True)
    check("日报 CSV 导出（带 BOM）", st == 200 and raw.startswith(b"\xef\xbb\xbf"), st)
    st, b, _ = req("GET", "/reports/attribution?dim=strategy", token=trader["access_token"])
    check("归因含策略/行业/标的三个维度", st == 200 and all(k in b["data"] for k in ("by_strategy", "by_industry", "by_symbol")), st)

    section("10. 资金出入金审批")
    st, b, _ = req("POST", "/capital/transfers", {"type": "deposit", "amount": 5000000, "reason": "追加资金"}, token=admin["access_token"])
    check("发起入金申请", st == 200 and b["data"]["status"] == "pending", (st, b))
    trid = b["data"]["id"] if st == 200 else ""
    st, b, _ = req("POST", f"/capital/transfers/{trid}/approve", {"approve": True}, token=admin["access_token"])
    check("申请人不能自审 403", st == 403, st)
    st, b, _ = req("POST", f"/capital/transfers/{trid}/approve", {"approve": True}, token=risk["access_token"])
    check("风控官审批入金成功", st == 200 and b["data"]["transfer"]["status"] == "approved", (st, b))

    section("11. 系统运维")
    st, b, _ = req("POST", "/system/tasks/T_RECON/run", None, token=admin["access_token"])
    check("手动触发对账任务", st == 200 and b["data"]["status"] == "success", st)
    st, b, _ = req("POST", "/reconcile/run", None, token=trader["access_token"])
    check("手动对账返回三账结果", st == 200 and "ok" in b["data"], st)
    st, b, _ = req("POST", "/system/halt", {"reason": "冒烟测试熔断"}, token=risk["access_token"])
    check("一键全停", st == 200 and b["data"]["status"] == "halt", st)
    st, b, _ = req("GET", "/strategies", token=admin["access_token"])
    check("熔断后策略全部 halt", all(s["status"] == "halt" for s in b["data"]), [s["status"] for s in b["data"]])
    st, b, _ = req("POST", "/system/resume", None, token=risk["access_token"])
    check("恢复交易", st == 200 and b["data"]["status"] == "open", st)

    section("12. 审计留痕")
    st, b, _ = req("GET", "/audit/logs?size=100", token=admin["access_token"])
    entries = b["data"] if st == 200 else []
    acts = {e["action"] for e in entries}
    need = {"order.create", "order.amend", "order.cancel", "risk.rule.approve",
            "strategy.params.approve", "capital.transfer.approve", "system.halt"}
    check("关键写操作全部留痕", need.issubset(acts), sorted(need - acts))
    check("审计含用户/IP/耗时/请求ID",
          bool(entries) and all(e.get("user_name") and e.get("ip") and "latency_ms" in e for e in entries[:5]))
    st, b, _ = req("GET", "/audit/logs?action=order.create", token=admin["access_token"])
    check("审计支持按动作过滤", st == 200 and all(e["action"] == "order.create" for e in b["data"]), st)

    section("13. 限流与错误处理")
    codes = []
    for _ in range(40):
        st, _, _ = req("POST", "/orders", {"symbol": "600519", "side": "BUY", "qty": 100, "price": 1690},
                       token=trader["access_token"])
        codes.append(st)
    check("写接口触发令牌桶限流 429", 429 in codes, set(codes))
    st, b, _ = req("GET", "/not-exist", token=admin["access_token"])
    check("未知路由 404", st == 404 and b["code"] == 1004, st)

    section("14. WebSocket 握手与首帧")
    ws_ok, ws_msg = ws_probe(admin["access_token"])
    check("WS 101 握手 + hello 帧", ws_ok, ws_msg)

    print(f"\n{'='*56}")
    print(f"  通过 {PASS} / 失败 {FAIL}")
    if FAILED:
        print("  失败项：")
        for f in FAILED:
            print("   -", f)
    print("=" * 56)
    return 0 if FAIL == 0 else 1


def ws_probe(token):
    """最小 WebSocket 客户端：握手 + 读一帧，验证服务端实现正确。"""
    try:
        host_port = BASE.replace("http://", "").replace("https://", "")
        host, _, port = host_port.partition(":")
        port = int(port or 80)
        key = base64.b64encode(os.urandom(16)).decode()
        s = socket.create_connection((host, port), timeout=5)
        reqs = (
            f"GET /api/v1/ws?token={token}&channels=overview HTTP/1.1\r\n"
            f"Host: {host}:{port}\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n"
            f"Sec-WebSocket-Key: {key}\r\nSec-WebSocket-Version: 13\r\n\r\n"
        )
        s.sendall(reqs.encode())
        head = b""
        while b"\r\n\r\n" not in head:
            chunk = s.recv(1024)
            if not chunk:
                return False, "connection closed"
            head += chunk
        if b"101" not in head.split(b"\r\n")[0]:
            return False, head.split(b"\r\n")[0].decode(errors="ignore")
        # 校验 Accept 计算
        expect = base64.b64encode(hashlib.sha1((key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11").encode()).digest()).decode()
        if expect.encode() not in head:
            return False, "bad Sec-WebSocket-Accept"
        # 读一帧
        hdr = s.recv(2)
        if len(hdr) < 2:
            return False, "no frame"
        ln = hdr[1] & 0x7F
        if ln == 126:
            ln = struct.unpack(">H", s.recv(2))[0]
        payload = b""
        while len(payload) < ln:
            payload += s.recv(ln - len(payload))
        data = json.loads(payload)
        s.close()
        return data.get("type") == "hello", data.get("type")
    except Exception as e:  # noqa: BLE001
        return False, f"{type(e).__name__}: {e}"


if __name__ == "__main__":
    sys.exit(main())
