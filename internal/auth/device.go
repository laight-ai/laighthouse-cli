package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// CLIClientID는 인증 서비스에 등록된 이 CLI의 공개 OAuth 클라이언트 ID다. 비밀키가 없다.
const CLIClientID = "laighthouse-cli"

const deviceCodeGrant = "urn:ietf:params:oauth:grant-type:device_code"

// ErrDeviceLoginUnavailable은 인증 서비스에 CLI 클라이언트가 등록돼 있지 않을 때(400 invalid_client) 돌려준다.
var ErrDeviceLoginUnavailable = errors.New("이 환경은 아직 브라우저 로그인을 지원하지 않습니다")

// DeviceCode는 디바이스 로그인을 시작하면 받는 값이다(RFC 8628 §3.2).
// 서버가 verification_uri_complete도 주지만 쓰지 않는다. 인증 서비스는 피싱을 막으려고
// ?user_code=로 코드를 미리 채우지 않고, 사용자가 코드를 직접 입력하게 한다.
type DeviceCode struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

// StartDeviceLogin은 브라우저 로그인(디바이스 코드 플로우)을 시작한다.
func (c *Client) StartDeviceLogin(ctx context.Context) (DeviceCode, error) {
	resp, err := c.postForm(ctx, "/auth/oauth/device_authorization", url.Values{"client_id": {CLIClientID}})
	if err != nil {
		return DeviceCode{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		err := errorFromResponse(resp)
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.Code == "invalid_client" {
			return DeviceCode{}, ErrDeviceLoginUnavailable
		}
		return DeviceCode{}, err
	}
	// interval이 없으면 RFC 8628 기본값 5초.
	d := DeviceCode{Interval: 5}
	if err := json.NewDecoder(resp.Body).Decode(&d); err != nil {
		return DeviceCode{}, err
	}
	return d, nil
}

// WaitDeviceLogin은 사용자가 브라우저에서 승인할 때까지 토큰 엔드포인트를 폴링한다.
func (c *Client) WaitDeviceLogin(ctx context.Context, d DeviceCode) (Tokens, error) {
	interval := time.Duration(d.Interval) * time.Second
	for {
		select {
		case <-ctx.Done():
			return Tokens{}, ctx.Err()
		case <-time.After(interval):
		}
		t, err := c.oauthToken(ctx, CLIClientID, url.Values{
			"grant_type":  {deviceCodeGrant},
			"device_code": {d.DeviceCode},
		})
		var apiErr *APIError
		if !errors.As(err, &apiErr) {
			return t, err
		}
		switch apiErr.Code {
		case "authorization_pending":
		case "slow_down":
			interval += 5 * time.Second
		case "access_denied":
			return Tokens{}, errors.New("브라우저에서 로그인 요청을 거부했습니다")
		case "expired_token":
			return Tokens{}, errors.New("코드가 만료됐습니다. 다시 로그인하세요")
		default:
			return Tokens{}, err
		}
	}
}

// oauthToken은 /auth/oauth/token에 grant를 보내고 JSON으로 받은 토큰 쌍을 돌려준다.
func (c *Client) oauthToken(ctx context.Context, clientID string, form url.Values) (Tokens, error) {
	form.Set("client_id", clientID)
	resp, err := c.postForm(ctx, "/auth/oauth/token", form)
	if err != nil {
		return Tokens{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Tokens{}, errorFromResponse(resp)
	}
	var body struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return Tokens{}, err
	}
	if body.AccessToken == "" || body.RefreshToken == "" {
		return Tokens{}, errors.New("응답에 토큰이 없습니다")
	}
	return Tokens{AccessToken: body.AccessToken, RefreshToken: body.RefreshToken, ClientID: clientID}, nil
}

func (c *Client) postForm(ctx context.Context, path string, form url.Values) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return c.http().Do(req)
}
