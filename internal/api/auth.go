package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const (
	cookieName  = "ycfrp_token"
	tokenTTL    = 7 * 24 * time.Hour
	bcryptCost  = 10
)

type tokenPayload struct {
	User  string `json:"u"`
	Issue int64  `json:"i"`
	Exp   int64  `json:"e"`
}

func (s *Server) signToken(user string) (string, error) {
	secret := s.app.Cfg.JWTSecret
	if secret == "" {
		return "", errors.New("服务端签名密钥缺失")
	}
	now := time.Now()
	payload, err := json.Marshal(tokenPayload{
		User:  user,
		Issue: now.Unix(),
		Exp:   now.Add(tokenTTL).Unix(),
	})
	if err != nil {
		return "", err
	}
	body := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return body + "." + sig, nil
}

func (s *Server) verifyToken(token string) (string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return "", errors.New("登录状态格式不正确")
	}
	secret := s.app.Cfg.JWTSecret
	if secret == "" {
		return "", errors.New("服务端签名密钥缺失")
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(parts[0]))
	expected := mac.Sum(nil)
	actual, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", errors.New("登录状态已损坏")
	}
	if subtle.ConstantTimeCompare(expected, actual) != 1 {
		return "", errors.New("登录状态校验失败")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return "", errors.New("登录状态已损坏")
	}
	var payload tokenPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return "", errors.New("登录状态已损坏")
	}
	if time.Now().Unix() > payload.Exp {
		return "", errors.New("登录已过期，请重新登录")
	}
	return payload.User, nil
}

// DefaultUsername is the bootstrap account name.
const DefaultUsername = "admin"

// DefaultPassword is the bootstrap password, kept in sync with the config
// package default and used by the CLI password reset command.
const DefaultPassword = "admin"

// HashPassword converts a plain text password into a bcrypt hash. It is
// exported so launchers can persist new credentials.
func HashPassword(plain string) (string, error) { return hashPassword(plain) }

// checkPassword validates a submitted password against the stored secret,
// transparently supporting both bcrypt hashes and the plain text default.
func checkPassword(stored, submitted string) bool {
	if strings.HasPrefix(stored, "$2a$") || strings.HasPrefix(stored, "$2b$") || strings.HasPrefix(stored, "$2y$") {
		return bcrypt.CompareHashAndPassword([]byte(stored), []byte(submitted)) == nil
	}
	return subtle.ConstantTimeCompare([]byte(stored), []byte(submitted)) == 1
}

// hashPassword converts a plain text password into a bcrypt hash.
func hashPassword(plain string) (string, error) {
	out, err := bcrypt.GenerateFromPassword([]byte(plain), bcryptCost)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func (s *Server) tokenFrom(r *http.Request) string {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
	}
	if c, err := r.Cookie(cookieName); err == nil {
		return c.Value
	}
	return ""
}

// requireAuth guards every management endpoint.
func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := s.tokenFrom(r)
		if token == "" {
			writeErr(w, http.StatusUnauthorized, "请先登录")
			return
		}
		user, err := s.verifyToken(token)
		if err != nil {
			writeErr(w, http.StatusUnauthorized, err.Error())
			return
		}
		if user != s.app.Cfg.Username {
			writeErr(w, http.StatusUnauthorized, "账号已变更，请重新登录")
			return
		}
		next(w, r)
	}
}
