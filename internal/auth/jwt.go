package auth

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// claims는 JWT 본문을 읽는다. 서명은 검증하지 않는다. 토큰의 유효성은 서버가 판단하고,
// CLI는 갱신 시점과 화면에 보일 사용자 정보를 정하는 데만 쓴다.
func claims(token string, v any) error {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return errors.New("JWT 형식이 아닙니다")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return err
	}
	return json.Unmarshal(payload, v)
}

// expiresAt은 JWT의 exp 클레임을 읽는다. 만료 전에 갱신할지 정하는 데 쓴다.
func expiresAt(token string) (time.Time, error) {
	var c struct {
		Exp int64 `json:"exp"`
	}
	if err := claims(token, &c); err != nil {
		return time.Time{}, err
	}
	return time.Unix(c.Exp, 0), nil
}

// User는 access 토큰에 든 사용자 정보다.
type User struct {
	UID   string   `json:"uid"`
	Email string   `json:"email"`
	Name  string   `json:"name"`
	Roles []string `json:"roles"`
}

// UserFromToken은 access 토큰에서 사용자 정보를 읽는다. 브라우저 로그인 토큰에는 웹 세션 ID가 없어
// 인증 서비스의 /auth/me가 받지 않으므로, 어느 로그인 방식이든 토큰에서 읽는다.
func UserFromToken(token string) (User, error) {
	var c struct {
		Sub   string   `json:"sub"`
		Email string   `json:"email"`
		Name  string   `json:"name"`
		Roles []string `json:"roles"`
	}
	if err := claims(token, &c); err != nil {
		return User{}, err
	}
	return User{UID: c.Sub, Email: c.Email, Name: c.Name, Roles: c.Roles}, nil
}
