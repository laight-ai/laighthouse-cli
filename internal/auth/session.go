package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/laight-ai/laighthouse-cli/internal/config"
)

// refreshLeeway만큼 만료가 남았으면 미리 갱신한다.
const refreshLeeway = 60 * time.Second

// Session은 저장된 토큰으로 요청에 Authorization 헤더를 붙이고, 만료가 가까우면 갱신하며,
// 그래도 401이 오면 한 번 더 갱신해 재시도한다.
type Session struct {
	Env    config.Env
	Store  *Store
	Client *Client
	Now    func() time.Time
	// Header는 요청마다 덧붙일 헤더다. 요청에 이미 있는 이름은 건드리지 않는다.
	Header http.Header

	tokens Tokens
	loaded bool
}

func (s *Session) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Session) load() error {
	if s.loaded {
		return nil
	}
	t, err := s.Store.Load(s.Env)
	if err != nil {
		return err
	}
	s.tokens = t
	s.loaded = true
	return nil
}

func (s *Session) refresh(ctx context.Context) error {
	t, err := s.Client.Refresh(ctx, s.tokens)
	if err != nil {
		// 쿠키 갱신은 401, OAuth 갱신은 400 invalid_grant로 refresh 토큰의 만료나 폐기를 알린다.
		var apiErr *APIError
		if errors.As(err, &apiErr) && (apiErr.Status == http.StatusUnauthorized || apiErr.Code == "invalid_grant") {
			return fmt.Errorf("세션이 만료됐습니다. %w", ErrNotLoggedIn)
		}
		return fmt.Errorf("토큰 갱신 실패: %w", err)
	}
	s.tokens = t
	return s.Store.Save(s.Env, t)
}

// AccessToken은 유효한 access 토큰을 돌려준다. 만료가 가까우면 먼저 갱신한다.
func (s *Session) AccessToken(ctx context.Context) (string, error) {
	if err := s.load(); err != nil {
		return "", err
	}
	exp, err := expiresAt(s.tokens.AccessToken)
	if err != nil || s.now().Add(refreshLeeway).After(exp) {
		if err := s.refresh(ctx); err != nil {
			return "", err
		}
	}
	return s.tokens.AccessToken, nil
}

// Do는 req에 Bearer 토큰을 붙여 보낸다. 401이면 토큰을 갱신하고 한 번 재시도한다.
// 재시도하려면 본문을 다시 읽을 수 있어야 하므로 req.GetBody가 없는 요청은 재시도하지 않는다.
func (s *Session) Do(req *http.Request) (*http.Response, error) {
	token, err := s.AccessToken(req.Context())
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	for k, v := range s.Header {
		if k = http.CanonicalHeaderKey(k); req.Header[k] == nil {
			req.Header[k] = v
		}
	}
	resp, err := s.Client.http().Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusUnauthorized || (req.Body != nil && req.GetBody == nil) {
		return resp, nil
	}
	resp.Body.Close()

	if err := s.refresh(req.Context()); err != nil {
		return nil, err
	}
	retry := req.Clone(req.Context())
	if req.GetBody != nil {
		if retry.Body, err = req.GetBody(); err != nil {
			return nil, err
		}
	}
	retry.Header.Set("Authorization", "Bearer "+s.tokens.AccessToken)
	return s.Client.http().Do(retry)
}

// Tokens는 현재 보관 중인 토큰 쌍이다. 로그아웃에 쓴다.
func (s *Session) Tokens() (Tokens, error) {
	if err := s.load(); err != nil {
		return Tokens{}, err
	}
	return s.tokens, nil
}
