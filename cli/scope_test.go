package cli

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// runScoped는 Options.Scope가 scope를 돌려주는 앱으로 명령을 실행한다.
func runScoped(t *testing.T, f *fakeBackend, scope string, args ...string) (string, error) {
	t.Helper()
	root, _ := newApp(Options{Scope: func() string { return scope }})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(io.Discard)
	root.SetArgs(append([]string{"--env", "dev", "--base-url", f.srv.URL}, args...))
	err := root.ExecuteContext(context.Background())
	return out.String(), err
}

// 기본 브랜드는 scope마다 따로 저장한다. scope에서 정한 브랜드가 scope 없는 실행(기존 사용자)에 섞이지
// 않고, scope에 기본 브랜드가 없으면 scope 없는 기본 브랜드로 넘어가지도 않는다.
func TestScopeSeparatesDefaultBrand(t *testing.T) {
	f := setup(t)
	if _, err := run(t, f, "brands", "use", "브랜드A"); err != nil {
		t.Fatal(err)
	}
	out, err := runScoped(t, f, "t9", "--json", "brands", "use")
	if err != nil || strings.TrimSpace(out) != "null" {
		t.Fatalf("scope에는 아직 기본 브랜드가 없어야 하는데 %q %v", out, err)
	}
	if _, err := runScoped(t, f, "t9", "reports"); err == nil || !strings.Contains(err.Error(), "brands use") {
		t.Fatalf("scope에 기본 브랜드가 없으면 'brands use'를 안내해야 하는데 %v", err)
	}

	if _, err := runScoped(t, f, "t9", "brands", "use", "브랜드B"); err != nil {
		t.Fatal(err)
	}
	if out, err := runScoped(t, f, "t9", "brands", "use"); err != nil || !strings.Contains(out, "b2") {
		t.Fatalf("scope의 기본 브랜드는 b2여야 하는데 %q %v", out, err)
	}
	if out, err := run(t, f, "brands", "use"); err != nil || !strings.Contains(out, "b1") {
		t.Fatalf("scope 없는 기본 브랜드는 b1 그대로여야 하는데 %q %v", out, err)
	}
	if _, err := runScoped(t, f, "t9", "reports"); err != nil {
		t.Fatal(err)
	}
	if len(f.seen("GET", "/api/resources/b2/reports")) != 1 {
		t.Error("scope에서는 scope의 기본 브랜드(b2) 보고서를 조회해야 합니다")
	}
	if out, err := runScoped(t, f, "t9", "brands"); err != nil || !strings.Contains(strings.Join(strings.Fields(out), " "), "* b2") {
		t.Errorf("brands 목록은 scope의 기본 브랜드(b2)에 표시해야 하는데 %q %v", out, err)
	}
	if out, err := runScoped(t, f, "t9", "whoami"); err != nil || !strings.Contains(out, "브랜드: 브랜드B [b2]") {
		t.Errorf("whoami는 scope의 기본 브랜드를 보여야 하는데 %q %v", out, err)
	}
}

// 서버가 대행 중이라고 알려 주면 whoami가 조직 옆에 표시한다.
func TestWhoamiShowsActing(t *testing.T) {
	f := setup(t)
	f.acting = true
	out, err := run(t, f, "whoami")
	if err != nil || !strings.Contains(out, "조직:   라이트 [t1] (member, 대행 중)") {
		t.Fatalf("대행 중 표시를 기대했는데 %q %v", out, err)
	}
	f.acting = false
	if out, err := run(t, f, "whoami"); err != nil || !strings.Contains(out, "조직:   라이트 [t1] (member)\n") {
		t.Fatalf("대행 중이 아니면 표시하지 않아야 하는데 %q %v", out, err)
	}
}

// scope마다 정한 기본 브랜드는 config.json 밖에 둔다. scope를 모르는 이전 버전이 config.json을 읽고
// 통째로 다시 써도(brands use) 지워지지 않아야 한다.
func TestScopedDefaultBrandSurvivesOldBinaryRewrite(t *testing.T) {
	f := setup(t)
	if _, err := runScoped(t, f, "t9", "brands", "use", "브랜드B"); err != nil {
		t.Fatal(err)
	}
	// 이전 버전의 brands use가 쓰는 모양 그대로 config.json을 덮어쓴다.
	p := filepath.Join(os.Getenv("LAIGHTHOUSE_CONFIG_DIR"), "config.json")
	old := `{"default_brand":{"dev":{"id":"b1","name":"브랜드A"}}}`
	if err := os.WriteFile(p, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := runScoped(t, f, "t9", "brands", "use"); err != nil || !strings.Contains(out, "b2") {
		t.Fatalf("scope의 기본 브랜드 b2가 남아 있어야 하는데 %q %v", out, err)
	}
	if out, err := run(t, f, "brands", "use"); err != nil || !strings.Contains(out, "b1") {
		t.Fatalf("이전 버전이 정한 기본 브랜드 b1을 읽어야 하는데 %q %v", out, err)
	}
}

// scope 안에서는 안내 문구에 scope를 함께 적는다. scope 밖에 값이 있어도 "이 환경에 없다"로 읽히지 않게 한다.
func TestScopedBrandsUseMessagesNameScope(t *testing.T) {
	f := setup(t)
	if out, err := runScoped(t, f, "t9", "brands", "use"); err != nil || !strings.Contains(out, "dev 환경(t9)에 기본 브랜드가 없습니다") {
		t.Fatalf("scope를 적은 안내를 기대했는데 %q %v", out, err)
	}
	if out, err := runScoped(t, f, "t9", "brands", "use", "브랜드B"); err != nil || !strings.Contains(out, "dev 환경(t9)의 기본 브랜드를 브랜드B [b2]") {
		t.Fatalf("scope를 적은 완료 안내를 기대했는데 %q %v", out, err)
	}
}
