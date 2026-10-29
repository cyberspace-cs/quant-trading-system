package api

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// ===================== 手写 JWT（HS256，零第三方依赖）=====================
//
// 为什么不用 github.com/golang-jwt/jwt？
//   1) 本仓库坚持「零外部依赖，go run 即可跑」的演示原则；
//   2) 面试时能手写 HS256 签名/校验，说明你真的懂 JWT 结构而不只是会调库。
//
// 结构：base64url(header) + "." + base64url(claims) + "." + base64url(HMAC-SHA256)

var b64 = base64.RawURLEncoding

// Claims JWT 载荷。生产可加 iss/aud/nbf/tenant，这里保留最小必要集合。
type Claims struct {
	Sub  string `json:"sub"`  // 用户 ID
	Name string `json:"name"` // 展示名
	Role string `json:"role"` // 角色（RBAC）
	Typ  string `json:"typ"`  // access / refresh
	Iat  int64  `json:"iat"`
	Exp  int64  `json:"exp"`
	Jti  string `json:"jti"` // 令牌唯一 ID（用于登出黑名单）
}

// Valid 是否在有效期内
func (c *Claims) Valid(now time.Time) bool {
	return c.Exp > now.Unix() && c.Sub != ""
}

// Sign 生成令牌
func Sign(secret []byte, c Claims) string {
	head := b64.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	payload, _ := json.Marshal(c)
	body := head + "." + b64.EncodeToString(payload)
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(body))
	return body + "." + b64.EncodeToString(mac.Sum(nil))
}

// ErrToken 令牌错误
var ErrToken = errors.New("invalid token")

// Parse 校验签名 + 有效期，返回载荷
func Parse(secret []byte, tok string) (*Claims, error) {
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return nil, ErrToken
	}
	body := parts[0] + "." + parts[1]
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(body))
	want := mac.Sum(nil)
	got, err := b64.DecodeString(parts[2])
	if err != nil {
		return nil, ErrToken
	}
	// 常量时间比较，防时序侧信道
	if subtle.ConstantTimeCompare(want, got) != 1 {
		return nil, errors.New("bad signature")
	}
	raw, err := b64.DecodeString(parts[1])
	if err != nil {
		return nil, ErrToken
	}
	var c Claims
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, ErrToken
	}
	if !c.Valid(time.Now()) {
		return nil, errors.New("token expired")
	}
	return &c, nil
}

// newJTI 生成令牌唯一 ID
func newJTI() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// hashPassword 演示用口令摘要：sha256(salt + password)。
// 生产必须换 bcrypt / argon2id（同仓库 docs 已注明），这里为保持零依赖。
func hashPassword(pw string) string {
	sum := sha256.Sum256([]byte("quant@salt::" + pw))
	return hex.EncodeToString(sum[:])
}

// checkPassword 常量时间比对
func checkPassword(pw, hash string) bool {
	return subtle.ConstantTimeCompare([]byte(hashPassword(pw)), []byte(hash)) == 1
}
