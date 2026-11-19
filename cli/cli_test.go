package cli

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/laight-ai/laighthouse-cli/internal/auth"
	"github.com/laight-ai/laighthouse-cli/internal/config"
)

// 모든 명령과 플래그에 도움말이 있어야 한다. 에이전트와 사람이 --help만으로 기능을 알 수 있게 하는 약속이다.
func TestEveryCommandAndFlagHasHelp(t *testing.T) {
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		if c.Short == "" {
			t.Errorf("%s: Short 설명이 없습니다", c.CommandPath())
		}
		c.LocalFlags().VisitAll(func(f *pflag.Flag) {
			if f.Usage == "" {
				t.Errorf("%s --%s: 플래그 설명이 없습니다", c.CommandPath(), f.Name)
			}
		})
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(newRoot("test"))
}

func TestBareNounAndSingularAliasList(t *testing.T) {
	f := setup(t)
	for _, args := range [][]string{{"brands"}, {"brand"}, {"brands", "list"}, {"brands", "ls"}} {
		out, err := run(t, f, append([]string{"--json"}, args...)...)
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		var got []map[string]any
		if err := json.Unmarshal([]byte(out), &got); err != nil || len(got) != 2 {
			t.Fatalf("%v: 브랜드 두 개의 JSON 배열을 기대했는데 %s", args, out)
		}
	}
}

func TestUnknownSubcommandFails(t *testing.T) {
	f := setup(t)
	for _, args := range [][]string{{"reports", "shwo", "x"}, {"members", "lst"}, {"teams", "foo"}, {"invite", "lst"}, {"upload", "histroy"}} {
		_, err := run(t, f, args...)
		if err == nil || !strings.Contains(err.Error(), "하위 명령이 아닙니다") {
			t.Errorf("%v: 하위 명령 오류를 기대했는데 %v", args, err)
		}
	}
	_, err := run(t, f, "upload", "histroy")
	if err == nil || !strings.Contains(err.Error(), "upload history") {
		t.Errorf("오타에 'upload history'를 제안해야 하는데 %v", err)
	}
}

func TestMembersAllFollowsCursor(t *testing.T) {
	f := setup(t)
	out, err := run(t, f, "--json", "members", "--all", "--limit", "1")
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Items     []map[string]any `json:"items"`
		NextAfter *string          `json:"next_after"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil || len(got.Items) != 2 || got.NextAfter != nil {
		t.Fatalf("두 페이지를 합쳐야 하는데: %s", out)
	}
	reqs := f.seen("GET", "/api/authz/members")
	if len(reqs) != 2 || !strings.Contains(reqs[1].query, "after=c1") {
		t.Fatalf("커서 요청이 예상과 다름: %+v", reqs)
	}
}

func TestMembersShowByEmailFragment(t *testing.T) {
	f := setup(t)
	out, err := run(t, f, "--json", "members", "show", "lee@")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "u2.marker") {
		t.Fatalf("lee@x.com(u2)의 권한을 조회해야 하는데: %s", out)
	}
}

// 멤버 권한 조회가 200인데 JSON이 아니면(프록시 오류 페이지 등) 패닉 없이 Content-Type을 담은 오류로 실패한다.
func TestMembersShowPermissionsNonJSONBody(t *testing.T) {
	f := setup(t)
	f.memberPermsHTML = true
	out, err := run(t, f, "members", "show", "lee@")
	if err == nil || !strings.Contains(err.Error(), "text/html") {
		t.Fatalf("Content-Type을 담은 오류를 기대했는데 %v", err)
	}
	if out != "" {
		t.Fatalf("실패하면 아무것도 출력하지 않아야 하는데: %s", out)
	}
}

func TestBrandRequiredWithoutDefault(t *testing.T) {
	f := setup(t)
	_, err := run(t, f, "reports")
	if err == nil || !strings.Contains(err.Error(), "brands use") {
		t.Fatalf("'brands use' 안내를 기대했는데 %v", err)
	}
}

func TestBrandsUseThenReportsWithoutFlag(t *testing.T) {
	f := setup(t)
	if _, err := run(t, f, "brands", "use", "브랜드B"); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, f, "brands", "use")
	if err != nil || !strings.Contains(out, "b2") {
		t.Fatalf("기본 브랜드 b2를 보여야 하는데 %q %v", out, err)
	}
	if _, err := run(t, f, "reports"); err != nil {
		t.Fatal(err)
	}
	if len(f.seen("GET", "/api/resources/b2/reports")) != 1 {
		t.Fatal("기본 브랜드(b2)의 보고서를 조회해야 합니다")
	}
	// --brand는 이름 일부로도 기본 브랜드보다 앞선다.
	if _, err := run(t, f, "reports", "--brand", "드A"); err != nil {
		t.Fatal(err)
	}
	if len(f.seen("GET", "/api/resources/b1/reports")) != 1 {
		t.Fatal("--brand 드A는 b1로 정해져야 합니다")
	}
}

// 그룹 노드도 이름으로 고르며, 같은 이름의 보관 노드(g9)는 후보에서 빠진다. 대상·역할·범위는 이름으로
// 보이고, 멤버 목록에 없는 대상(u9)은 UID로 남는다.
func TestBrandsAccessShowsNames(t *testing.T) {
	f := setup(t)
	out, err := run(t, f, "brands", "access", "그룹1")
	if err != nil {
		t.Fatal(err)
	}
	if len(f.seen("GET", "/api/authz/resources/g1/access")) != 1 {
		t.Fatal("그룹1(g1)의 접근 목록을 조회해야 합니다")
	}
	for _, want := range []string{"kim@x.com", "마케터", "조직 전체", "그로스팀", "Owner", "그룹1", "2026-12-31", "u9"} {
		if !strings.Contains(out, want) {
			t.Errorf("출력에 %q가 없습니다:\n%s", want, out)
		}
	}
}

func TestBrandsAccessUsesDefaultBrand(t *testing.T) {
	f := setup(t)
	if _, err := run(t, f, "brands", "access"); err == nil || !strings.Contains(err.Error(), "brands use") {
		t.Fatalf("기본 브랜드가 없으면 'brands use'를 안내해야 하는데 %v", err)
	}
	if _, err := run(t, f, "brands", "use", "브랜드A"); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, f, "--json", "brands", "access")
	if err != nil {
		t.Fatal(err)
	}
	if len(f.seen("GET", "/api/authz/resources/b1/access")) != 1 || !strings.Contains(out, `"subject_id": "tm1"`) {
		t.Fatalf("기본 브랜드(b1)의 접근 목록을 JSON으로 내야 하는데: %s", out)
	}
	// JSON은 이름을 풀지 않으므로 트리를 받지 않는다.
	for _, r := range f.seen("GET", "/api/authz/resources") {
		if r.path == "/api/authz/resources" {
			t.Fatal("인자 없는 --json에서는 트리를 조회하지 않아야 합니다")
		}
	}
	out, err = run(t, f, "brands", "access")
	if err != nil || !strings.Contains(out, "그로스팀") || len(f.seen("GET", "/api/authz/resources/b1/access")) != 2 {
		t.Fatalf("표로도 기본 브랜드(b1)의 접근 목록을 이름으로 보여야 하는데 %v\n%s", err, out)
	}
}

func TestBrandsAccessDuplicateNameNeedsID(t *testing.T) {
	f := setup(t)
	_, err := run(t, f, "brands", "access", "기타")
	if err == nil || !strings.Contains(err.Error(), "여러 개") || !strings.Contains(err.Error(), "e1") || !strings.Contains(err.Error(), "e2") {
		t.Fatalf("이름이 같은 노드 둘(e1, e2)은 모호하다고 해야 하는데 %v", err)
	}
	if _, err := run(t, f, "brands", "access", "e2"); err != nil {
		t.Fatal(err)
	}
	if len(f.seen("GET", "/api/authz/resources/e2/access")) != 1 {
		t.Fatal("ID로는 e2를 골라야 합니다")
	}
}

// 이름 조회가 실패하면 ID만 찍힌 표 대신 오류로 끝낸다.
func TestBrandsAccessLookupFailureIsError(t *testing.T) {
	f := setup(t)
	f.membersFail = true
	_, err := run(t, f, "brands", "access", "b1")
	if err == nil || !strings.Contains(err.Error(), "HTTP 500") {
		t.Fatalf("멤버 목록 500을 오류로 내야 하는데 %v", err)
	}
}

func TestBrandAmbiguous(t *testing.T) {
	f := setup(t)
	_, err := run(t, f, "reports", "--brand", "브랜드")
	if err == nil || !strings.Contains(err.Error(), "여러 개") {
		t.Fatalf("여러 개가 맞는다는 오류를 기대했는데 %v", err)
	}
}

func TestReportsAllWalksPages(t *testing.T) {
	f := setup(t)
	out, err := run(t, f, "--json", "reports", "--brand", "b1", "--all")
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Reports []map[string]any `json:"reports"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil || len(got.Reports) != 2 {
		t.Fatalf("두 페이지를 합쳐야 하는데: %s", out)
	}
}

func TestUploadPicksOnlyExcelConnection(t *testing.T) {
	f := setup(t)
	file := filepath.Join(t.TempDir(), "sales.csv")
	if err := os.WriteFile(file, []byte("a,b\n1,2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, f, "--json", "upload", file, "--brand", "브랜드A", "--date", "2026-10-01")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out, `"upload_id": 42`) {
		t.Fatalf("202 본문을 출력해야 하는데: %s", out)
	}
	// 매체 이름으로 골라도 같은 연결이다.
	if _, err := run(t, f, "upload", file, "--brand", "b1", "--date", "2026-10-01", "--connection", "naver"); err != nil {
		t.Fatal(err)
	}
}

func TestUploadRejectsWrongExtension(t *testing.T) {
	f := setup(t)
	_, err := run(t, f, "upload", "/tmp/x.txt", "--brand", "b1")
	if err == nil || !strings.Contains(err.Error(), ".xlsx 또는 .csv") {
		t.Fatalf("확장자 오류를 기대했는데 %v", err)
	}
}

func TestInviteByEmailAndRoleName(t *testing.T) {
	f := setup(t)
	out, err := run(t, f, "--json", "invite", "kim@x.com", "lee@x.com", "--role", "마케터", "--brand", "브랜드A", "--ttl", "3d")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	reqs := f.seen("POST", "/api/authz/invites")
	if len(reqs) != 2 {
		t.Fatalf("이메일마다 한 번씩 보내야 하는데 %d번", len(reqs))
	}
	var body map[string]any
	_ = json.Unmarshal(reqs[0].body, &body)
	if body["role_id"] != "r1" || body["scope_type"] != "resource" || body["token_ttl_seconds"] != float64(3*24*3600) ||
		body["email_or_domain_restriction"] != "kim@x.com" {
		t.Fatalf("요청 본문이 예상과 다름: %s", reqs[0].body)
	}
	if ids, _ := body["scope_resource_ids"].([]any); len(ids) != 1 || ids[0] != "b1" {
		t.Fatalf("브랜드A는 b1로 정해져야 함: %s", reqs[0].body)
	}
}

func TestInviteAlreadyInvited(t *testing.T) {
	f := setup(t)
	_, err := run(t, f, "invite", "dup@x.com", "--role", "마케터")
	if err == nil {
		t.Fatal("중복 초대는 실패해야 합니다")
	}
	out, _ := run(t, f, "invite", "dup@x.com", "--role", "마케터")
	if !strings.Contains(out, "invite resend old1") {
		t.Fatalf("재발송 안내가 있어야 하는데: %s", out)
	}
	if _, err := run(t, f, "invite", "dup@x.com", "--role", "마케터", "--replace"); err != nil {
		t.Fatalf("--replace면 기존 초대를 바꿔 보내야 하는데 %v", err)
	}
}

func TestInviteRejectsNonEmail(t *testing.T) {
	f := setup(t)
	_, err := run(t, f, "invite", "kim", "--role", "마케터")
	if err == nil || !strings.Contains(err.Error(), "@") {
		t.Fatalf("이메일 형식 오류를 기대했는데 %v", err)
	}
}

func TestAPIGetPassthrough(t *testing.T) {
	f := setup(t)
	out, err := run(t, f, "api", "GET", "/api/authz/me/permissions")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"member.read"`) {
		t.Fatalf("응답 본문을 그대로 내야 하는데: %s", out)
	}
}

func TestForbiddenShowsDetailAndReason(t *testing.T) {
	f := setup(t)
	f.teamsForbidden = true
	_, err := run(t, f, "teams")
	if err == nil || !strings.Contains(err.Error(), "권한 없음(403)") || !strings.Contains(err.Error(), "missing_permission") {
		t.Fatalf("403 detail/reason을 기대했는데 %v", err)
	}
}

// 토큰 교환이 로그인 세션 종료로 거절되면 일반 인증 실패와 구분해 안내한다.
func TestSessionEndedIsDistinguished(t *testing.T) {
	resp := &http.Response{StatusCode: 401, Header: http.Header{}}
	for _, body := range []string{
		`{"detail":"session_ended: the subject_token's login session has ended; sign in again"}`,
		`{"detail":"Login session ended","reason":"session_ended"}`,
	} {
		err := CheckStatus(resp, []byte(body))
		if err == nil || !strings.Contains(err.Error(), "로그인 세션이 끝났습니다") {
			t.Errorf("%s: 세션 종료 안내를 기대했는데 %v", body, err)
		}
	}
	err := CheckStatus(resp, []byte(`{"detail":"Invalid credentials"}`))
	if err == nil || !strings.Contains(err.Error(), "인증 실패(401)") {
		t.Errorf("일반 401은 그대로여야 하는데 %v", err)
	}
}

func TestParseTTL(t *testing.T) {
	for in, want := range map[string]float64{"7d": 168, "12h": 12, "90m": 1.5} {
		d, err := parseTTL(in)
		if err != nil || d.Hours() != want {
			t.Errorf("%s: %v %v", in, d, err)
		}
	}
	if _, err := parseTTL("일주일"); err == nil {
		t.Error("잘못된 기간은 오류여야 합니다")
	}
}

func TestBrowserLoginSavesOAuthTokens(t *testing.T) {
	f := setup(t)
	out, err := run(t, f, "login", "--no-browser")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "kim@x.com 계정으로 로그인했습니다") {
		t.Fatalf("토큰의 이메일로 로그인 안내를 기대했는데: %s", out)
	}
	store := &auth.Store{Path: filepath.Join(os.Getenv("LAIGHTHOUSE_CONFIG_DIR"), "credentials.json")}
	saved, err := store.Load(config.EnvDev)
	if err != nil || saved.ClientID != auth.CLIClientID || saved.RefreshToken != "r2" {
		t.Fatalf("브라우저 로그인 토큰이 client_id와 함께 저장돼야 하는데 %+v %v", saved, err)
	}
}

func TestBrowserLoginUnavailableSuggestsPassword(t *testing.T) {
	f := setup(t)
	f.noCLIClient = true
	_, err := run(t, f, "login", "--no-browser")
	if err == nil || !strings.Contains(err.Error(), "login --password") {
		t.Fatalf("--password 안내를 기대했는데 %v", err)
	}
}
