package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/laight-ai/laighthouse-cli/internal/config"
)

// fakeJWT는 exp만 든 서명 없는 JWT를 만든다. 서버 역할도 테스트가 하므로 서명이 필요 없다.
func fakeJWT(t *testing.T, exp time.Time) string {
	t.Helper()
	payload, _ := json.Marshal(map[string]any{"exp": exp.Unix()})
	enc := base64.RawURLEncoding.EncodeToString
	return enc([]byte(`{"alg":"none"}`)) + "." + enc(payload) + ".sig"
}

// fakeAuth는 인증 서비스와 보호된 API 하나를 흉내 낸다.
type fakeAuth struct {
	t            *testing.T
	now          time.Time
	validAccess  map[string]bool
	validRefresh map[string]bool
	refreshes    int
	issued       int

	// OAuth(브라우저 로그인) 쪽
	unknownClient  bool // CLI 클라이언트가 등록되지 않은 환경
	pending        int  // 승인 전까지 authorization_pending을 몇 번 줄지
	denied         bool
	polls          int
	oauthRefreshes int
	revoked        url.Values
}

func (f *fakeAuth) newTokens() Tokens {
	f.issued++
	tk := Tokens{
		AccessToken:  fakeJWT(f.t, f.now.Add(time.Hour)),
		RefreshToken: fmt.Sprintf("refresh-%d", f.issued),
	}
	f.validAccess[tk.AccessToken] = true
	f.validRefresh[tk.RefreshToken] = true
	return tk
}

// oauthError는 RFC 6749 §5.2 형식의 오류를 쓴다.
func oauthError(w http.ResponseWriter, code string) {
	w.WriteHeader(http.StatusBadRequest)
	fmt.Fprintf(w, `{"error":%q}`, code)
}

func (f *fakeAuth) issue(w http.ResponseWriter) Tokens {
	tk := f.newTokens()
	http.SetCookie(w, &http.Cookie{Name: cookieAccess, Value: tk.AccessToken, HttpOnly: true, Path: "/"})
	http.SetCookie(w, &http.Cookie{Name: cookieRefresh, Value: tk.RefreshToken, HttpOnly: true, Path: "/"})
	return tk
}

func (f *fakeAuth) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /auth/token", func(w http.ResponseWriter, r *http.Request) {
		if r.FormValue("grant_type") != "password" || r.FormValue("username") != "me@laight.ai" || r.FormValue("password") != "pw" {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"detail":"Invalid credentials"}`)
			return
		}
		f.issue(w)
		fmt.Fprint(w, `{"message":"ok","token_type":"bearer"}`)
	})
	mux.HandleFunc("POST /auth/refresh", func(w http.ResponseWriter, r *http.Request) {
		ck, err := r.Cookie(cookieRefresh)
		if err != nil || !f.validRefresh[ck.Value] {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"detail":"Invalid refresh token"}`)
			return
		}
		delete(f.validRefresh, ck.Value) // 회전
		f.refreshes++
		f.issue(w)
	})
	mux.HandleFunc("POST /auth/oauth/device_authorization", func(w http.ResponseWriter, r *http.Request) {
		if f.unknownClient || r.FormValue("client_id") != CLIClientID {
			oauthError(w, "invalid_client")
			return
		}
		fmt.Fprint(w, `{"device_code":"dc","user_code":"ABCD-EFGH","verification_uri":"https://x/auth/oauth/device",
			"verification_uri_complete":"https://x/auth/oauth/device?user_code=ABCD-EFGH","expires_in":1800,"interval":0}`)
	})
	mux.HandleFunc("POST /auth/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		if r.FormValue("client_id") != CLIClientID {
			oauthError(w, "invalid_client")
			return
		}
		switch r.FormValue("grant_type") {
		case "urn:ietf:params:oauth:grant-type:device_code":
			f.polls++
			switch {
			case r.FormValue("device_code") != "dc":
				oauthError(w, "invalid_grant")
				return
			case f.denied:
				oauthError(w, "access_denied")
				return
			case f.pending > 0:
				f.pending--
				oauthError(w, "authorization_pending")
				return
			}
		case "refresh_token":
			if !f.validRefresh[r.FormValue("refresh_token")] {
				oauthError(w, "invalid_grant")
				return
			}
			delete(f.validRefresh, r.FormValue("refresh_token")) // 회전
			f.oauthRefreshes++
		default:
			oauthError(w, "unsupported_grant_type")
			return
		}
		tk := f.newTokens()
		json.NewEncoder(w).Encode(map[string]any{
			"token_type": "Bearer", "access_token": tk.AccessToken, "refresh_token": tk.RefreshToken, "expires_in": 3600, "scope": "",
		})
	})
	mux.HandleFunc("POST /auth/oauth/revoke", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		f.revoked = r.PostForm
	})
	mux.HandleFunc("GET /api/thing", func(w http.ResponseWriter, r *http.Request) {
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !f.validAccess[tok] {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		fmt.Fprint(w, `{"ok":true}`)
	})
	return mux
}

func newTestSession(t *testing.T) (*fakeAuth, *Session, *httptest.Server) {
	t.Helper()
	f := &fakeAuth{t: t, now: time.Now(), validAccess: map[string]bool{}, validRefresh: map[string]bool{}}
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	store := &Store{Path: filepath.Join(t.TempDir(), "credentials.json")}
	s := &Session{
		Env:    config.EnvDev,
		Store:  store,
		Client: &Client{BaseURL: srv.URL, HTTP: srv.Client()},
		Now:    func() time.Time { return f.now },
	}
	return f, s, srv
}

func TestLoginStoresTokensFromCookies(t *testing.T) {
	f, s, _ := newTestSession(t)
	tk, err := s.Client.Login(context.Background(), "me@laight.ai", "pw")
	if err != nil {
		t.Fatal(err)
	}
	if !f.validAccess[tk.AccessToken] || tk.RefreshToken != "refresh-1" {
		t.Fatalf("쿠키에서 토큰을 못 꺼냄: %+v", tk)
	}
	if err := s.Store.Save(config.EnvDev, tk); err != nil {
		t.Fatal(err)
	}
	got, err := s.Store.Load(config.EnvDev)
	if err != nil || got != tk {
		t.Fatalf("저장/복원 불일치: %+v %v", got, err)
	}
}

func TestLoginWrongPassword(t *testing.T) {
	_, s, _ := newTestSession(t)
	_, err := s.Client.Login(context.Background(), "me@laight.ai", "bad")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 401 || apiErr.Detail != "Invalid credentials" {
		t.Fatalf("401 detail을 기대했는데 %v", err)
	}
}

func TestNotLoggedIn(t *testing.T) {
	_, s, srv := newTestSession(t)
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/thing", nil)
	_, err := s.Do(req)
	if err == nil || !strings.Contains(err.Error(), ErrNotLoggedIn.Error()) {
		t.Fatalf("ErrNotLoggedIn을 기대했는데 %v", err)
	}
}

func TestRefreshesBeforeExpiry(t *testing.T) {
	f, s, srv := newTestSession(t)
	tk, _ := s.Client.Login(context.Background(), "me@laight.ai", "pw")
	s.Store.Save(config.EnvDev, tk)

	f.now = f.now.Add(59*time.Minute + 30*time.Second) // 만료 30초 전
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/thing", nil)
	resp, err := s.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 || f.refreshes != 1 {
		t.Fatalf("미리 갱신 후 성공을 기대했는데 status=%d refreshes=%d", resp.StatusCode, f.refreshes)
	}
	saved, _ := s.Store.Load(config.EnvDev)
	if saved.RefreshToken != "refresh-2" {
		t.Fatalf("회전된 refresh 토큰이 저장되지 않음: %+v", saved)
	}
}

func TestRetriesOnceAfter401(t *testing.T) {
	f, s, srv := newTestSession(t)
	tk, _ := s.Client.Login(context.Background(), "me@laight.ai", "pw")
	s.Store.Save(config.EnvDev, tk)
	// 서버가 세션을 끊어 access 토큰이 무효해졌지만 refresh는 아직 유효한 상황.
	delete(f.validAccess, tk.AccessToken)

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/thing", nil)
	resp, err := s.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 || f.refreshes != 1 {
		t.Fatalf("401 후 갱신·재시도를 기대했는데 status=%d refreshes=%d", resp.StatusCode, f.refreshes)
	}
}

func TestExpiredRefreshBecomesNotLoggedIn(t *testing.T) {
	f, s, srv := newTestSession(t)
	tk, _ := s.Client.Login(context.Background(), "me@laight.ai", "pw")
	s.Store.Save(config.EnvDev, tk)
	delete(f.validAccess, tk.AccessToken)
	delete(f.validRefresh, tk.RefreshToken)

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/thing", nil)
	_, err := s.Do(req)
	if err == nil || !strings.Contains(err.Error(), "세션이 만료") {
		t.Fatalf("세션 만료 오류를 기대했는데 %v", err)
	}
}
