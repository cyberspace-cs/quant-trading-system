package api

import (
	"strings"
	"sync"
	"time"
)

// ===================== 认证 + RBAC =====================

// User 用户
type User struct {
	ID     string `json:"id"`
	Name   string `json:"username"`
	Role   string `json:"role"`
	Descr  string `json:"descr"`
	Active bool   `json:"active"`
	pwHash string
}

// TokenPair 令牌对
type TokenPair struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	Role         string `json:"role"`
	Name         string `json:"name"`
}

// 权限点（越细越利于审计；这里 8 个覆盖全部写操作）
const (
	PermRead       = "read"
	PermOrderWrite = "order:write"
	PermStratWrite = "strategy:write"
	PermBacktest   = "backtest:write"
	PermFactor     = "factor:write"
	PermRiskWrite  = "risk:write"
	PermCapital    = "capital:write"
	PermApprove    = "approve"
	PermSystem     = "system:write"
)

// rbac 角色 -> 权限集合。生产落 MySQL role/permission 表，这里用矩阵表达同样语义。
var rbac = map[string]map[string]bool{
	"admin": {
		PermRead: true, PermOrderWrite: true, PermStratWrite: true, PermBacktest: true,
		PermFactor: true, PermRiskWrite: true, PermCapital: true, PermApprove: true, PermSystem: true,
	},
	"trader": {
		PermRead: true, PermOrderWrite: true, PermStratWrite: true, PermBacktest: true,
	},
	"researcher": {
		PermRead: true, PermFactor: true, PermBacktest: true,
	},
	"risk": {
		PermRead: true, PermRiskWrite: true, PermApprove: true,
	},
	"viewer": {
		PermRead: true,
	},
}

// Can 判断角色是否有某权限
func Can(role, perm string) bool {
	m, ok := rbac[role]
	if !ok {
		return false
	}
	return m[perm]
}

// PermsOf 返回角色的权限清单（前端据此隐藏按钮，服务端仍然强校验）
func PermsOf(role string) []string {
	out := []string{}
	for p := range rbac[role] {
		out = append(out, p)
	}
	return out
}

// Auth 认证中心
type Auth struct {
	mu       sync.RWMutex
	secret   []byte
	users    map[string]*User
	revoked  map[string]int64 // jti -> 过期时间（登出黑名单）
	accessTT time.Duration
	refreshT time.Duration
}

// NewAuth 初始化（内置 5 个演示账号，覆盖 5 种角色）
func NewAuth(secret string) *Auth {
	a := &Auth{
		secret: []byte(secret), users: map[string]*User{}, revoked: map[string]int64{},
		accessTT: 45 * time.Minute, refreshT: 7 * 24 * time.Hour,
	}
	add := func(id, name, role, descr, pw string) {
		a.users[name] = &User{ID: id, Name: name, Role: role, Descr: descr, Active: true, pwHash: hashPassword(pw)}
	}
	add("U1001", "admin", "admin", "系统管理员 · 全权限", "Admin@123")
	add("U1002", "trader", "trader", "交易员 · 下单/改撤/策略开关", "Trader@123")
	add("U1003", "risk", "risk", "风控官 · 规则变更/审批", "Risk@123")
	add("U1004", "research", "researcher", "研究员 · 因子/回测", "Research@123")
	add("U1005", "viewer", "viewer", "只读观察员", "Viewer@123")
	return a
}

// SetTTL 调整令牌有效期（测试用）
func (a *Auth) SetTTL(access, refresh time.Duration) {
	a.accessTT, a.refreshT = access, refresh
}

// LoginResult 登录结果
type LoginResult struct {
	TokenPair
	User      *User    `json:"user"`
	Perms     []string `json:"perms"`
	ExpiresAt int64    `json:"expires_at"`
}

// Login 账号口令登录
func (a *Auth) Login(username, password string) (*LoginResult, error) {
	a.mu.RLock()
	u, ok := a.users[strings.ToLower(strings.TrimSpace(username))]
	a.mu.RUnlock()
	if !ok || !u.Active || !checkPassword(password, u.pwHash) {
		return nil, ErrToken
	}
	return a.issue(u, true), nil
}

func (a *Auth) issue(u *User, withRefresh bool) *LoginResult {
	now := time.Now()
	acc := Claims{
		Sub: u.ID, Name: u.Name, Role: u.Role, Typ: "access",
		Iat: now.Unix(), Exp: now.Add(a.accessTT).Unix(), Jti: newJTI(),
	}
	res := &LoginResult{
		TokenPair: TokenPair{
			AccessToken: Sign(a.secret, acc), TokenType: "Bearer",
			ExpiresIn: int(a.accessTT.Seconds()), Role: u.Role, Name: u.Name,
		},
		User:      &User{ID: u.ID, Name: u.Name, Role: u.Role, Descr: u.Descr, Active: u.Active},
		Perms:     PermsOf(u.Role),
		ExpiresAt: now.Add(a.accessTT).Unix(),
	}
	if withRefresh {
		ref := Claims{
			Sub: u.ID, Name: u.Name, Role: u.Role, Typ: "refresh",
			Iat: now.Unix(), Exp: now.Add(a.refreshT).Unix(), Jti: newJTI(),
		}
		res.RefreshToken = Sign(a.secret, ref)
	}
	return res
}

// Refresh 用 refresh token 换新的 access token（refresh 轮换，旧 token 作废）
func (a *Auth) Refresh(refresh string) (*LoginResult, error) {
	c, err := Parse(a.secret, refresh)
	if err != nil {
		return nil, err
	}
	if c.Typ != "refresh" {
		return nil, ErrToken
	}
	if a.IsRevoked(c.Jti) {
		return nil, ErrToken
	}
	a.Revoke(c.Jti, c.Exp)
	a.mu.RLock()
	u := a.findByID(c.Sub)
	a.mu.RUnlock()
	if u == nil || !u.Active {
		return nil, ErrToken
	}
	return a.issue(u, true), nil
}

func (a *Auth) findByID(id string) *User {
	for _, u := range a.users {
		if u.ID == id {
			return u
		}
	}
	return nil
}

// UserByID 供 /auth/me 使用
func (a *Auth) UserByID(id string) *User {
	a.mu.RLock()
	defer a.mu.RUnlock()
	u := a.findByID(id)
	if u == nil {
		return nil
	}
	cp := *u
	cp.pwHash = ""
	return &cp
}

// Verify 解析 access token
func (a *Auth) Verify(tok string) (*Claims, error) {
	c, err := Parse(a.secret, tok)
	if err != nil {
		return nil, err
	}
	if c.Typ != "access" {
		return nil, ErrToken
	}
	if a.IsRevoked(c.Jti) {
		return nil, ErrToken
	}
	return c, nil
}

// Revoke 拉黑令牌（登出）
func (a *Auth) Revoke(jti string, exp int64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.revoked == nil {
		a.revoked = map[string]int64{}
	}
	a.revoked[jti] = exp
	// 顺手清理已过期的黑名单，避免无限增长
	now := time.Now().Unix()
	for k, v := range a.revoked {
		if v < now {
			delete(a.revoked, k)
		}
	}
}

// IsRevoked 是否已登出
func (a *Auth) IsRevoked(jti string) bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	_, ok := a.revoked[jti]
	return ok
}

// Users 用户列表（脱敏，管理端用）
func (a *Auth) Users() []*User {
	a.mu.RLock()
	defer a.mu.RUnlock()
	out := make([]*User, 0, len(a.users))
	for _, u := range a.users {
		cp := *u
		cp.pwHash = ""
		out = append(out, &cp)
	}
	return out
}
