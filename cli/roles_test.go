package cli

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func postedRole(t *testing.T, f *fakeBackend, i int) map[string]any {
	t.Helper()
	reqs := f.seen("POST", "/api/authz/roles")
	if len(reqs) <= i {
		t.Fatalf("역할 생성 요청이 %d번뿐입니다", len(reqs))
	}
	var body map[string]any
	if err := json.Unmarshal(reqs[i].body, &body); err != nil {
		t.Fatal(err)
	}
	return body
}

// --all은 서버 권한 목록의 키를 모두 주고, 외부 역할이면 외부에 줄 수 있는 키만 준다.
func TestRolesCreateAllFollowsRegistry(t *testing.T) {
	f := setup(t)
	if _, err := run(t, f, "roles", "create", "관리자", "--all"); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, f, "roles", "create", "대행사", "--all", "--external", "--scope-expandable"); err != nil {
		t.Fatal(err)
	}

	internal := postedRole(t, f, 0)
	if want := []any{"report.read", "member.invite", "report.export"}; !reflect.DeepEqual(internal["permission_keys"], want) {
		t.Fatalf("내부 역할은 권한 목록 전부여야 하는데 %v", internal["permission_keys"])
	}
	if internal["name"] != "관리자" || internal["is_external"] != false || internal["scope_expandable"] != false {
		t.Fatalf("내부 역할 본문이 예상과 다름: %v", internal)
	}

	external := postedRole(t, f, 1)
	if want := []any{"report.read", "report.export"}; !reflect.DeepEqual(external["permission_keys"], want) {
		t.Fatalf("외부 역할은 external_grantable 키만이어야 하는데 %v", external["permission_keys"])
	}
	if external["is_external"] != true || external["scope_expandable"] != true {
		t.Fatalf("외부 역할 플래그가 본문에 없음: %v", external)
	}
}

func TestRolesCreatePermissionFlag(t *testing.T) {
	f := setup(t)
	out, err := run(t, f, "roles", "create", "마케터2", "-p", "report.read,report.export", "-p", "dashboard.manage")
	if err != nil {
		t.Fatal(err)
	}
	body := postedRole(t, f, 0)
	if want := []any{"report.read", "report.export", "dashboard.manage"}; !reflect.DeepEqual(body["permission_keys"], want) {
		t.Fatalf("쉼표와 반복 지정을 모두 받아야 하는데 %v", body["permission_keys"])
	}
	if len(f.seen("GET", "/api/authz/permissions")) != 0 {
		t.Fatal("--permission이면 권한 목록을 조회할 필요가 없습니다")
	}
	if !strings.Contains(out, "r9") || !strings.Contains(out, "3개") {
		t.Fatalf("만든 역할의 ID와 권한 수를 보여 줘야 하는데: %s", out)
	}
}

// 요청을 보내기 전에 거절해야 하는 입력.
func TestRolesCreateRejectsBeforeRequest(t *testing.T) {
	f := setup(t)
	cases := map[string][]string{
		"권한 없음":               {"roles", "create", "빈역할"},
		"내부 역할에 범위 확장":        {"roles", "create", "x", "-p", "report.read", "--scope-expandable"},
		"--all과 --permission": {"roles", "create", "x", "--all", "-p", "report.read"},
	}
	for name, args := range cases {
		if _, err := run(t, f, args...); err == nil {
			t.Errorf("%s: 실패해야 합니다", name)
		}
	}
	if n := len(f.seen("POST", "/api/authz/roles")); n != 0 {
		t.Fatalf("요청을 보내지 않아야 하는데 %d번 보냄", n)
	}
}

// --dry-run은 쓰기 요청을 보내지 않고, 실제로 보낼 메서드·경로·본문을 보여 준다.
func TestRolesDryRunSendsNothing(t *testing.T) {
	f := setup(t)

	out, err := run(t, f, "--json", "roles", "create", "관리자", "--all", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	var plan struct {
		Method, Path string
		Body         map[string]any
	}
	if err := json.Unmarshal([]byte(out), &plan); err != nil {
		t.Fatalf("--json이면 JSON 하나여야 하는데: %s", out)
	}
	if plan.Method != "POST" || plan.Path != "/api/authz/roles" || len(plan.Body["permission_keys"].([]any)) != 3 {
		t.Fatalf("보낼 요청이 예상과 다름: %+v", plan)
	}

	out, err = run(t, f, "roles", "update", "마케터", "--name", "마케팅", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "PATCH /api/authz/roles/r1") || !strings.Contains(out, `"name": "마케팅"`) {
		t.Fatalf("update 요청을 보여 줘야 하는데: %s", out)
	}

	out, err = run(t, f, "roles", "delete", "마케터", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "DELETE /api/authz/roles/r1") {
		t.Fatalf("delete 요청을 보여 줘야 하는데: %s", out)
	}

	for _, m := range []string{"POST", "PATCH", "DELETE"} {
		if n := len(f.seen(m, "/api/authz/roles")); n != 0 {
			t.Fatalf("--dry-run인데 %s 요청을 %d번 보냄", m, n)
		}
	}
}

// 이름만 바꿀 때 권한 키를 함께 보내면 서버가 역할의 권한을 비운다.
func TestRolesUpdateNameOnlyKeepsPermissions(t *testing.T) {
	f := setup(t)
	if _, err := run(t, f, "roles", "update", "마케터", "--name", "마케팅"); err != nil {
		t.Fatal(err)
	}
	reqs := f.seen("PATCH", "/api/authz/roles/")
	if len(reqs) != 1 || reqs[0].path != "/api/authz/roles/r1" {
		t.Fatalf("이름으로 고른 역할 r1을 고쳐야 하는데 %+v", reqs)
	}
	var body map[string]any
	_ = json.Unmarshal(reqs[0].body, &body)
	if want := map[string]any{"name": "마케팅"}; !reflect.DeepEqual(body, want) {
		t.Fatalf("본문에는 이름만 있어야 하는데 %s", reqs[0].body)
	}
}

func TestRolesUpdatePermissionsReplace(t *testing.T) {
	f := setup(t)
	if _, err := run(t, f, "roles", "update", "r1", "-p", "report.read"); err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	_ = json.Unmarshal(f.seen("PATCH", "/api/authz/roles/r1")[0].body, &body)
	if want := map[string]any{"permission_keys": []any{"report.read"}}; !reflect.DeepEqual(body, want) {
		t.Fatalf("권한 키만 교체해야 하는데 %v", body)
	}
	if _, err := run(t, f, "roles", "update", "r1"); err == nil {
		t.Fatal("바꿀 것이 없으면 실패해야 합니다")
	}
}

func TestRolesDeleteByName(t *testing.T) {
	f := setup(t)
	out, err := run(t, f, "roles", "delete", "마케터")
	if err != nil {
		t.Fatal(err)
	}
	if reqs := f.seen("DELETE", "/api/authz/roles/"); len(reqs) != 1 || reqs[0].path != "/api/authz/roles/r1" {
		t.Fatalf("r1을 지워야 하는데 %+v", reqs)
	}
	if !strings.Contains(out, "마케터") {
		t.Fatalf("지운 역할을 알려 줘야 하는데: %s", out)
	}
}

func TestRolesShowListsKeys(t *testing.T) {
	f := setup(t)
	out, err := run(t, f, "roles", "show", "마케터")
	if err != nil {
		t.Fatal(err)
	}
	if len(f.seen("GET", "/api/authz/roles/r1")) != 1 {
		t.Fatal("이름으로 고른 r1을 조회해야 합니다")
	}
	if !strings.Contains(out, "권한 키 (2개)") || !strings.Contains(out, "  report.export\n") {
		t.Fatalf("권한 키를 한 줄씩 보여 줘야 하는데: %s", out)
	}
}
