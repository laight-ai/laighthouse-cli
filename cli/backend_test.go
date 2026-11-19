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
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/laight-ai/laighthouse-cli/internal/auth"
	"github.com/laight-ai/laighthouse-cli/internal/config"
)

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
	needApprover    bool // 결재 정책이 승인자 지정을 요구해 approval 없는 초대가 409
	badApprover     bool // approval이 있는 초대가 400 approver_designation
	brandOnlyPerms  bool // 내 권한이 브랜드 범위뿐이고 조직 전체 권한이 없음
	acting          bool // /api/authz/me가 대행 중인 조직을 돌려줌
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
			{"id":"r0","name":"Owner","is_external":false,"is_owner":true,"permission_keys":[],"scope_expandable":false},
			{"id":"r2","name":"대행사","is_external":true,"is_owner":false,"permission_keys":["report.read"],"scope_expandable":false}],"next_after":null}`)
	})
	handle("GET /api/authz/permissions", func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		item := `{"key":%q,"description":"","scope_types":["tenant"],"external_grantable":%t,"risk":"low","ui_bundle":"basic","acting_tier":"never","acting_enabled":false,"acting_grant_only":false}`
		// 내 권한(GET /api/authz/me/permissions)과 맞춰 둔다: member.read·role.manage는 조직 전체에서,
		// report.read는 브랜드 b1에서만 가졌고, member.invite는 갖지 않았다.
		fmt.Fprintf(w, `{"items":[`+item+`,`+item+`,`+item+`,`+item+`]}`,
			"report.read", true, "member.read", true, "member.invite", false, "role.manage", false)
	})
	handle("POST /api/authz/roles", func(w http.ResponseWriter, _ *http.Request, body []byte) {
		var in map[string]any
		_ = json.Unmarshal(body, &in)
		keys, _ := json.Marshal(in["permission_keys"])
		w.WriteHeader(201)
		fmt.Fprintf(w, `{"id":"r9","name":%q,"is_external":%t,"is_owner":false,"permission_keys":%s,"scope_expandable":%t}`,
			in["name"], in["is_external"] == true, keys, in["scope_expandable"] == true)
	})
	handle("PATCH /api/authz/roles/{id}", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		fmt.Fprintf(w, `{"id":%q,"name":"마케팅","is_external":false,"is_owner":false,"permission_keys":["report.read"],"scope_expandable":false}`, r.PathValue("id"))
	})
	handle("DELETE /api/authz/roles/{id}", func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		w.WriteHeader(204)
	})
	handle("POST /api/authz/invites", func(w http.ResponseWriter, _ *http.Request, body []byte) {
		var in map[string]any
		_ = json.Unmarshal(body, &in)
		if f.badApprover && in["approval"] != nil {
			w.WriteHeader(400)
			fmt.Fprint(w, `{"detail":"Every designated approver must be able to approve this request","reason":"approver_designation"}`)
			return
		}
		if f.needApprover && in["approval"] == nil {
			w.WriteHeader(409)
			fmt.Fprint(w, `{"detail":"approvers must be designated","reason":"approver_designation_required","required_approvals":1,"min_designated":1,
				"candidates":[{"uid":"u2","name":"이","email":"lee@x.com"}]}`)
			return
		}
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
		if f.brandOnlyPerms {
			fmt.Fprint(w, `{"items":[{"scope_type":"resource","scope_resource_id":"b1","permission_keys":["report.read","role.manage"]}]}`)
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
		fmt.Fprintf(w, `{"tenant_id":"t1","name":"라이트","kind":"member","tenant_kind":"customer","acting":%t}`, f.acting)
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
