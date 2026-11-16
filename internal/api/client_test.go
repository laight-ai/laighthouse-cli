package api_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/laight-ai/laighthouse-cli/internal/api"
	"github.com/laight-ai/laighthouse-cli/internal/auth"
	"github.com/laight-ai/laighthouse-cli/internal/config"
)

// 생성된 클라이언트가 auth.Session을 통해 Bearer 헤더를 붙여 /api 경로를 부르는지 확인한다.
func TestGeneratedClientUsesSession(t *testing.T) {
	const token = "eyJhbGciOiJub25lIn0.eyJleHAiOjQxMDI0NDQ4MDB9.sig" // exp 2100년
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/resources", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `[{"uid":"b1","name":"브랜드1"}]`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	store := &auth.Store{Path: filepath.Join(t.TempDir(), "credentials.json")}
	if err := store.Save(config.EnvDev, auth.Tokens{AccessToken: token, RefreshToken: "r"}); err != nil {
		t.Fatal(err)
	}
	sess := &auth.Session{Env: config.EnvDev, Store: store, Client: &auth.Client{BaseURL: srv.URL, HTTP: srv.Client()}}

	c, err := api.New(srv.URL, sess)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.ListBrandWithResponse(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode() != 200 || resp.JSON200 == nil || len(*resp.JSON200) != 1 {
		t.Fatalf("status=%d json=%v", resp.StatusCode(), resp.JSON200)
	}
}
