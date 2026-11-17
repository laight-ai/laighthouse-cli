package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

// testToken은 2100년에 만료되는 서명 없는 access 토큰이다.
var testToken = func() string {
	enc := base64.RawURLEncoding.EncodeToString
	payload, _ := json.Marshal(map[string]any{"exp": 4102444800, "sub": "u1", "email": "kim@x.com", "name": "김", "roles": []string{"user"}})
	return enc([]byte(`{"alg":"none"}`)) + "." + enc(payload) + ".sig"
}()

// fakeBackend는 백엔드 API를 흉내 낸다. 받은 요청을 기록해 테스트가 경로와 본문을 확인한다.
type fakeBackend struct {
	srv             *httptest.Server
	mu              sync.Mutex
	reqs            []recorded
	noCLIClient     bool // 인증 서비스에 CLI 클라이언트가 아직 등록되지 않은 환경
	teamsForbidden  bool // 팀 목록이 403
	membersFail     bool // 멤버 목록이 500
	noMembership    bool // 소속 조직이 없어 /api/authz/me와 내 권한 조회가 403
	myPermsHTML     bool // 내 권한 조회가 200인데 JSON이 아닌 본문
	memberPermsHTML bool // 멤버 권한 조회가 200인데 JSON이 아닌 본문
}

type recorded struct {
	method, path, query string
	body                []byte
}

func (f *fakeBackend) seen(method, pathPrefix string) []recorded {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []recorded
	for _, r := range f.reqs {
		if r.method == method && strings.HasPrefix(r.path, pathPrefix) {
			out = append(out, r)
		}
	}
	return out
}

// setup은 가짜 백엔드를 띄우고, 빈 설정 디렉터리에 로그인 토큰을 넣어 둔다.
func setup(t *testing.T) *fakeBackend {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("LAIGHTHOUSE_CONFIG_DIR", dir)
	t.Setenv("LAIGHTHOUSE_BRAND", "")
	store := &auth.Store{Path: filepath.Join(dir, "credentials.json")}
	if err := store.Save(config.EnvDev, auth.Tokens{AccessToken: testToken, RefreshToken: "r"}); err != nil {
		t.Fatal(err)
	}

	f := &fakeBackend{}
	mux := http.NewServeMux()
	handle := func(pattern string, h func(w http.ResponseWriter, r *http.Request, body []byte)) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer "+testToken {
				w.WriteHeader(401)
				fmt.Fprint(w, `{"detail":"Invalid credentials"}`)
				return
			}
			body, _ := io.ReadAll(r.Body)
			f.mu.Lock()
			f.reqs = append(f.reqs, recorded{r.Method, r.URL.Path, r.URL.RawQuery, body})
			f.mu.Unlock()
			r.Body = io.NopCloser(bytes.NewReader(body))
			w.Header().Set("Content-Type", "application/json")
			h(w, r, body)
		})
	}
	handle("GET /api/resources", func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		fmt.Fprint(w, `[{"uid":"b1","name":"브랜드A","brand_type":"own","country":"KR","language":"ko","enabled_features":["report_enabled"]},
			{"uid":"b2","name":"브랜드B","brand_type":"own","country":"KR","language":"ko","enabled_features":[]}]`)
	})
	handle("GET /api/authz/members", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		if f.membersFail {
			w.WriteHeader(500)
			fmt.Fprint(w, `{"detail":"boom"}`)
			return
		}
		if r.URL.Query().Get("after") == "" {
			fmt.Fprint(w, `{"items":[{"user_uid":"u1","email":"kim@x.com","name":"김","kind":"member","status":"active","joined_at":"2026-01-01T00:00:00Z","guest_scope_resource_ids":[]}],"next_after":"c1"}`)
			return
		}
		fmt.Fprint(w, `{"items":[{"user_uid":"u2","email":"lee@x.com","name":"이","kind":"guest","status":"active","joined_at":"2026-02-01T00:00:00Z","expires_at":"2026-12-31T00:00:00Z","guest_scope_resource_ids":["b1"]}],"next_after":null}`)
	})
	handle("GET /api/authz/members/{uid}/permissions", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		if f.memberPermsHTML {
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, `<html></html>`)
			return
		}
		fmt.Fprintf(w, `{"items":[{"scope_type":"tenant","scope_resource_id":null,"permission_keys":["report.read","%s.marker"]}]}`, r.PathValue("uid"))
	})
	handle("GET /api/authz/roles", func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		fmt.Fprint(w, `{"items":[{"id":"r1","name":"마케터","is_external":false,"is_owner":false,"permission_keys":["report.read"],"scope_expandable":false},
			{"id":"r0","name":"Owner","is_external":false,"is_owner":true,"permission_keys":[],"scope_expandable":false}],"next_after":null}`)
	})
	handle("POST /api/authz/invites", func(w http.ResponseWriter, _ *http.Request, body []byte) {
		var in map[string]any
		_ = json.Unmarshal(body, &in)
		if in["email_or_domain_restriction"] == "dup@x.com" && in["replaces_invite_id"] != "old1" {
			w.WriteHeader(409)
			fmt.Fprint(w, `{"detail":"already invited","reason":"already_invited","invite_id":"old1"}`)
			return
		}
		w.WriteHeader(201)
		fmt.Fprintf(w, `{"id":"i1","token":"tok","role_id":"%s","scope_type":"%s","scope_resource_ids":[],"expires_at":"2026-11-06T00:00:00Z",
			"issued_at":"2026-10-30T00:00:00Z","issued_by":"u1","max_uses":1,"used_count":0,"status":"active","binding_expires_at":null,
			"email_or_domain_restriction":"%s","last_mail_status":"queued","last_mail_requested_at":null,"resend_available_at":null}`,
			in["role_id"], in["scope_type"], in["email_or_domain_restriction"])
	})
	handle("GET /api/resources/{brand}/reports", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		page := r.URL.Query().Get("page")
		fmt.Fprintf(w, `{"uid":"%s","name":"브랜드","country":"KR","language":"ko","page":%s,"page_size":1,"total":2,
			"reports":[{"uid":"rep%s","name":"일간 보고서","type":"daily","reference_date":"2026-10-01","created":"2026-10-02","created_by":"김"}]}`,
			r.PathValue("brand"), page, page)
	})
	handle("GET /api/resources/{brand}/media-connections", func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		fmt.Fprint(w, `[{"id":7,"kind":"excel","media":"naver","source":"excel","status":"active"},{"id":8,"kind":"oauth","media":"meta","source":"meta","status":"active"}]`)
	})
	handle("POST /api/resources/{brand}/data-uploads", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			w.WriteHeader(400)
			return
		}
		file, hdr, err := r.FormFile("file")
		if err != nil {
			w.WriteHeader(400)
			return
		}
		defer file.Close()
		content, _ := io.ReadAll(file)
		if r.FormValue("connection_id") != "7" || r.FormValue("report_date") != "2026-10-01" || hdr.Filename != "sales.csv" || string(content) != "a,b\n1,2\n" {
			w.WriteHeader(422)
			fmt.Fprintf(w, `{"detail":"bad form: conn=%s date=%s"}`, r.FormValue("connection_id"), r.FormValue("report_date"))
			return
		}
		w.WriteHeader(202)
		fmt.Fprint(w, `{"upload_id":42,"run_id":9,"status":"processing"}`)
	})
	handle("GET /api/authz/me/permissions", func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		if f.noMembership {
			w.WriteHeader(403)
			fmt.Fprint(w, `{"detail":"Access denied"}`)
			return
		}
		if f.myPermsHTML {
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, `<html></html>`)
			return
		}
		fmt.Fprint(w, `{"items":[{"scope_type":"tenant","scope_resource_id":null,"permission_keys":["member.read","role.manage"]},
			{"scope_type":"resource","scope_resource_id":"b1","permission_keys":["report.read"]}]}`)
	})
	handle("GET /api/authz/me", func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		if f.noMembership {
			w.WriteHeader(403)
			fmt.Fprint(w, `{"detail":"Access denied"}`)
			return
		}
		fmt.Fprint(w, `{"tenant_id":"t1","name":"라이트","kind":"member","tenant_kind":"customer","acting":false}`)
	})
	mux.HandleFunc("POST /auth/oauth/device_authorization", func(w http.ResponseWriter, _ *http.Request) {
		if f.noCLIClient {
			w.WriteHeader(400)
			fmt.Fprint(w, `{"error":"invalid_client"}`)
			return
		}
		fmt.Fprint(w, `{"device_code":"dc","user_code":"ABCD-EFGH","verification_uri":"https://x/d","expires_in":1800,"interval":0}`)
	})
	mux.HandleFunc("POST /auth/oauth/token", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"token_type":"Bearer","access_token":%q,"refresh_token":"r2","expires_in":3600,"scope":""}`, testToken)
	})
	handle("GET /api/authz/resources", func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		fmt.Fprint(w, `{"levels":[],"items":[{"id":"g1","depth":0,"parent_id":null,"name":"그룹1","status":"active"},
			{"id":"b1","depth":1,"parent_id":"g1","name":"브랜드A","status":"active"},
			{"id":"g9","depth":0,"parent_id":null,"name":"그룹1","status":"archived"},
			{"id":"e1","depth":1,"parent_id":"g1","name":"기타","status":"active"},
			{"id":"e2","depth":0,"parent_id":null,"name":"기타","status":"active"}]}`)
	})
	handle("GET /api/authz/resources/{id}/access", func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		fmt.Fprint(w, `{"items":[
			{"id":"bd1","subject_type":"user","subject_id":"u1","role_id":"r1","scope_type":"tenant","scope_resource_id":null,"expires_at":null,"granted_by":null,"granted_at":"2026-01-01T00:00:00Z","source":"direct"},
			{"id":"bd2","subject_type":"team","subject_id":"tm1","role_id":"r0","scope_type":"resource","scope_resource_id":"g1","expires_at":"2026-12-31T00:00:00Z","granted_by":"u1","granted_at":"2026-02-01T00:00:00Z","source":"direct"},
			{"id":"bd3","subject_type":"user","subject_id":"u9","role_id":"r1","scope_type":"tenant","scope_resource_id":null,"expires_at":null,"granted_by":null,"granted_at":"2026-03-01T00:00:00Z","source":"direct"}],
			"next_after":null}`)
	})
	handle("GET /api/authz/teams", func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		if f.teamsForbidden {
			w.WriteHeader(403)
			fmt.Fprint(w, `{"detail":"Permission denied","reason":"missing_permission"}`)
			return
		}
		fmt.Fprint(w, `{"items":[{"id":"tm1","name":"그로스팀","member_count":3}],"next_after":null}`)
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

// run은 명령을 실행하고 stdout과 오류를 돌려준다. 같은 테스트 안에서는 설정 디렉터리를 공유한다.
func run(t *testing.T, f *fakeBackend, args ...string) (string, error) {
	t.Helper()
	root := newRoot("test")
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(io.Discard)
	root.SetArgs(append([]string{"--env", "dev", "--base-url", f.srv.URL}, args...))
	err := root.ExecuteContext(context.Background())
	return out.String(), err
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
