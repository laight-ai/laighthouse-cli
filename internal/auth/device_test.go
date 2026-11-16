package auth

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/laight-ai/laighthouse-cli/internal/config"
)

func deviceLogin(t *testing.T, s *Session) Tokens {
	t.Helper()
	ctx := context.Background()
	d, err := s.Client.StartDeviceLogin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	tk, err := s.Client.WaitDeviceLogin(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	return tk
}

func TestDeviceLoginPollsUntilApproved(t *testing.T) {
	f, s, _ := newTestSession(t)
	f.pending = 2
	d, err := s.Client.StartDeviceLogin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// 서버가 verification_uri_complete를 줘도 코드를 직접 입력하는 주소만 쓴다.
	if d.VerificationURI != "https://x/auth/oauth/device" || d.UserCode != "ABCD-EFGH" {
		t.Fatalf("코드 입력 주소와 기기 코드를 기대했는데 %+v", d)
	}
	tk, err := s.Client.WaitDeviceLogin(context.Background(), d)
	if err != nil {
		t.Fatal(err)
	}
	if f.polls != 3 || tk.ClientID != CLIClientID || !f.validAccess[tk.AccessToken] {
		t.Fatalf("승인 전 2번 대기 후 토큰을 기대했는데 polls=%d %+v", f.polls, tk)
	}
}

func TestDeviceLoginUnknownClient(t *testing.T) {
	f, s, _ := newTestSession(t)
	f.unknownClient = true
	_, err := s.Client.StartDeviceLogin(context.Background())
	if !errors.Is(err, ErrDeviceLoginUnavailable) {
		t.Fatalf("ErrDeviceLoginUnavailable을 기대했는데 %v", err)
	}
}

func TestDeviceLoginDenied(t *testing.T) {
	f, s, _ := newTestSession(t)
	f.denied = true
	d, _ := s.Client.StartDeviceLogin(context.Background())
	_, err := s.Client.WaitDeviceLogin(context.Background(), d)
	if err == nil || !strings.Contains(err.Error(), "거부") {
		t.Fatalf("거부 오류를 기대했는데 %v", err)
	}
}

// 브라우저 로그인 토큰은 쿠키 엔드포인트가 아니라 /auth/oauth/token으로 갱신하고, 로그아웃 때 폐기한다.
func TestDeviceTokensRefreshAndRevokeViaOAuth(t *testing.T) {
	f, s, srv := newTestSession(t)
	tk := deviceLogin(t, s)
	s.Store.Save(config.EnvDev, tk)

	f.now = f.now.Add(59*time.Minute + 30*time.Second)
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/thing", nil)
	resp, err := s.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 || f.oauthRefreshes != 1 || f.refreshes != 0 {
		t.Fatalf("OAuth 갱신을 기대했는데 status=%d oauth=%d cookie=%d", resp.StatusCode, f.oauthRefreshes, f.refreshes)
	}
	saved, _ := s.Store.Load(config.EnvDev)
	if saved.ClientID != CLIClientID || saved.RefreshToken == tk.RefreshToken {
		t.Fatalf("회전된 토큰과 client_id가 저장돼야 하는데 %+v", saved)
	}

	if err := s.Client.Logout(context.Background(), saved); err != nil {
		t.Fatal(err)
	}
	if f.revoked.Get("token") != saved.RefreshToken || f.revoked.Get("token_type_hint") != "refresh_token" || f.revoked.Get("client_id") != CLIClientID {
		t.Fatalf("refresh 토큰 폐기 요청을 기대했는데 %v", f.revoked)
	}
}

func TestDeviceExpiredRefreshBecomesNotLoggedIn(t *testing.T) {
	f, s, srv := newTestSession(t)
	tk := deviceLogin(t, s)
	s.Store.Save(config.EnvDev, tk)
	delete(f.validAccess, tk.AccessToken)
	delete(f.validRefresh, tk.RefreshToken)

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/thing", nil)
	_, err := s.Do(req)
	if err == nil || !errors.Is(err, ErrNotLoggedIn) {
		t.Fatalf("invalid_grant는 로그인 필요로 안내해야 하는데 %v", err)
	}
}

func TestUserFromToken(t *testing.T) {
	enc := base64.RawURLEncoding.EncodeToString
	token := enc([]byte(`{"alg":"none"}`)) + "." + enc([]byte(`{"sub":"u1","email":"kim@x.com","name":"김","roles":["user"],"exp":1}`)) + ".sig"
	u, err := UserFromToken(token)
	if err != nil || u.UID != "u1" || u.Email != "kim@x.com" || u.Name != "김" || len(u.Roles) != 1 {
		t.Fatalf("토큰에서 사용자를 못 읽음: %+v %v", u, err)
	}
}
