package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// 인증 서비스가 쓰는 쿠키 이름.
const (
	cookieAccess  = "access_token"
	cookieRefresh = "refresh_token"
)

// Client는 인증 서비스({BaseURL}/auth)를 부른다.
type Client struct {
	BaseURL string
	HTTP    *http.Client
}

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

// Login은 비밀번호 로그인(resource owner password grant)으로 토큰을 받는다.
// 인증 서비스는 토큰을 HttpOnly 쿠키로만 주므로 Set-Cookie에서 꺼낸다.
// Google/Microsoft로만 가입한 계정은 비밀번호가 없어 이 방식으로 로그인할 수 없다.
func (c *Client) Login(ctx context.Context, username, password string) (Tokens, error) {
	form := url.Values{
		"grant_type": {"password"},
		"username":   {username},
		"password":   {password},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/auth/token", strings.NewReader(form.Encode()))
	if err != nil {
		return Tokens{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.http().Do(req)
	if err != nil {
		return Tokens{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Tokens{}, errorFromResponse(resp)
	}
	return tokensFromCookies(resp)
}

// Refresh는 refresh 토큰으로 새 토큰 쌍을 받는다. 인증 서비스는 refresh 토큰을 회전시키므로
// 돌려받은 쌍을 반드시 저장해야 한다.
func (c *Client) Refresh(ctx context.Context, t Tokens) (Tokens, error) {
	if t.ClientID != "" {
		return c.oauthToken(ctx, t.ClientID, url.Values{
			"grant_type":    {"refresh_token"},
			"refresh_token": {t.RefreshToken},
		})
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/auth/refresh", nil)
	if err != nil {
		return Tokens{}, err
	}
	req.AddCookie(&http.Cookie{Name: cookieRefresh, Value: t.RefreshToken})
	resp, err := c.http().Do(req)
	if err != nil {
		return Tokens{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Tokens{}, errorFromResponse(resp)
	}
	return tokensFromCookies(resp)
}

// Logout은 서버의 세션을 끝낸다. 실패해도 로컬 토큰은 지워야 하므로 오류만 돌려준다.
// 브라우저 로그인 토큰은 refresh 토큰을 폐기(RFC 7009)하면 그 로그인 세션이 끝난다.
func (c *Client) Logout(ctx context.Context, t Tokens) error {
	if t.ClientID != "" {
		resp, err := c.postForm(ctx, "/auth/oauth/revoke", url.Values{
			"token":           {t.RefreshToken},
			"token_type_hint": {"refresh_token"},
			"client_id":       {t.ClientID},
		})
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return errorFromResponse(resp)
		}
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/auth/logout", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+t.AccessToken)
	req.AddCookie(&http.Cookie{Name: cookieRefresh, Value: t.RefreshToken})
	resp, err := c.http().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return errorFromResponse(resp)
	}
	return nil
}

func tokensFromCookies(resp *http.Response) (Tokens, error) {
	var t Tokens
	for _, ck := range resp.Cookies() {
		switch ck.Name {
		case cookieAccess:
			t.AccessToken = ck.Value
		case cookieRefresh:
			t.RefreshToken = ck.Value
		}
	}
	if t.AccessToken == "" || t.RefreshToken == "" {
		return Tokens{}, errors.New("응답에 토큰 쿠키가 없습니다")
	}
	return t, nil
}

// APIError는 2xx가 아닌 응답이다. detail은 인증 서비스와 백엔드가 공통으로 쓰는 오류 본문 필드다.
// OAuth 엔드포인트(/auth/oauth/*)는 RFC 6749 형식으로 답하므로 error를 Code에, error_description을 Detail에 담는다.
type APIError struct {
	Status int
	Code   string
	Detail string
}

func (e *APIError) Error() string {
	if e.Detail == "" {
		return fmt.Sprintf("HTTP %d", e.Status)
	}
	return fmt.Sprintf("HTTP %d: %s", e.Status, e.Detail)
}

func errorFromResponse(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	e := &APIError{Status: resp.StatusCode}
	var parsed struct {
		Detail      json.RawMessage `json:"detail"`
		Error       string          `json:"error"`
		Description string          `json:"error_description"`
	}
	ok := json.Unmarshal(body, &parsed) == nil
	switch {
	case ok && parsed.Error != "":
		e.Code, e.Detail = parsed.Error, parsed.Description
		if e.Detail == "" {
			e.Detail = parsed.Error
		}
	case ok && len(parsed.Detail) > 0:
		var s string
		if json.Unmarshal(parsed.Detail, &s) == nil {
			e.Detail = s
		} else {
			e.Detail = string(parsed.Detail)
		}
	case len(body) > 0:
		e.Detail = strings.TrimSpace(string(body))
	}
	return e
}
