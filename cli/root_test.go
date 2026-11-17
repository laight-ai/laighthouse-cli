package cli

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/spf13/pflag"
)

// Options.Flags로 붙인 전역 플래그를 Options.Header가 읽어, 생성 클라이언트와 api 명령의 요청 모두에 헤더로
// 싣는다. 플래그를 비우면 헤더를 보내지 않고, api -H로 같은 헤더를 주면 그 값만 보낸다.
func TestOptionsHeaderIsSentWithEveryRequest(t *testing.T) {
	setup(t) // 로그인 토큰만 쓴다
	var mu sync.Mutex
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got = append(got, r.URL.Path+"="+strings.Join(r.Header.Values("X-Test-Scope"), ","))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"items":[],"next_after":null}`)
	}))
	defer srv.Close()

	runWith := func(args ...string) {
		t.Helper()
		var scope string
		root, _ := newApp(Options{
			Flags: func(fs *pflag.FlagSet) { fs.StringVar(&scope, "scope", "", "시험용 헤더 값") },
			Header: func() http.Header {
				if scope == "" {
					return nil
				}
				return http.Header{"X-Test-Scope": {scope}}
			},
		})
		root.SetOut(io.Discard)
		root.SetErr(io.Discard)
		root.SetArgs(append([]string{"--env", "dev", "--base-url", srv.URL}, args...))
		if err := root.ExecuteContext(context.Background()); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
	runWith("--scope", "t1", "roles")
	runWith("--scope", "t1", "api", "GET", "/x")
	runWith("api", "GET", "/x")
	runWith("--scope", "t1", "api", "GET", "/x", "-H", "X-Test-Scope: t2")

	want := []string{"/api/authz/roles=t1", "/x=t1", "/x=", "/x=t2"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("요청별 헤더가 %v여야 하는데 %v", want, got)
	}
}
