package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/laight-ai/laighthouse-cli/internal/auth"
)

// whoami는 /auth/me(브라우저 로그인 토큰을 거부함) 대신 토큰에서 사용자를 읽는다.
func TestWhoamiReadsUserFromToken(t *testing.T) {
	f := setup(t)
	out, err := run(t, f, "--json", "whoami")
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		User   auth.User `json:"user"`
		Tenant struct {
			Name string `json:"name"`
		} `json:"tenant"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil || got.User.Email != "kim@x.com" || got.User.UID != "u1" || got.Tenant.Name != "라이트" {
		t.Fatalf("토큰의 사용자와 조직을 기대했는데 %s", out)
	}
}

// whoami --permissions는 계정 정보 아래에 내 권한 키를 범위별로 한 줄씩 묶어 보여 준다.
func TestWhoamiPermissionsGroupsByScope(t *testing.T) {
	f := setup(t)
	out, err := run(t, f, "whoami", "--permissions")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "kim@x.com") {
		t.Fatalf("계정 정보도 그대로 나와야 하는데: %s", out)
	}
	rows := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		if scope, keys, ok := strings.Cut(line, "  "); ok {
			rows[strings.TrimSpace(scope)] = strings.TrimSpace(keys)
		}
	}
	if rows["조직 전체"] != "member.read, role.manage" || rows["브랜드 b1"] != "report.read" {
		t.Fatalf("범위별 권한 키를 기대했는데: %s", out)
	}
}

func TestWhoamiPermissionsJSON(t *testing.T) {
	f := setup(t)
	out, err := run(t, f, "--json", "whoami", "--permissions")
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		User        auth.User `json:"user"`
		Permissions struct {
			Items []struct {
				ScopeType       string   `json:"scope_type"`
				ScopeResourceID *string  `json:"scope_resource_id"`
				PermissionKeys  []string `json:"permission_keys"`
			} `json:"items"`
		} `json:"permissions"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("JSON이어야 하는데 %v: %s", err, out)
	}
	items := got.Permissions.Items
	if got.User.Email != "kim@x.com" || len(items) != 2 ||
		items[0].ScopeType != "tenant" || strings.Join(items[0].PermissionKeys, ",") != "member.read,role.manage" ||
		items[1].ScopeType != "resource" || items[1].ScopeResourceID == nil || *items[1].ScopeResourceID != "b1" ||
		strings.Join(items[1].PermissionKeys, ",") != "report.read" {
		t.Fatalf("사용자와 범위별 권한 응답을 그대로 기대했는데: %s", out)
	}
}

// 소속 조직이 없으면 서버가 /api/authz/me와 내 권한 조회를 모두 403으로 거절한다. --permissions를 줘도
// 계정 정보는 보여 주고, 권한은 조회하지 않고 "없음"으로 표시한다.
func TestWhoamiPermissionsWithoutMembership(t *testing.T) {
	f := setup(t)
	f.noMembership = true
	out, err := run(t, f, "whoami", "--permissions")
	if err != nil {
		t.Fatalf("소속 조직이 없어도 성공해야 하는데 %v", err)
	}
	for _, want := range []string{"계정:   kim@x.com (김)", "조직:   없음", "권한:   없음"} {
		if !strings.Contains(out, want) {
			t.Errorf("%q가 출력에 있어야 하는데:\n%s", want, out)
		}
	}
	if n := len(f.seen("GET", "/api/authz/me/permissions")); n != 0 {
		t.Errorf("소속 조직이 없으면 권한을 조회하지 않아야 하는데 %d번 불렀습니다", n)
	}

	out, err = run(t, f, "--json", "whoami", "--permissions")
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if p, ok := got["permissions"]; !ok || string(p) != "null" {
		t.Errorf("--permissions를 주면 JSON에 permissions가 null로 있어야 하는데: %s", out)
	}
}

// 내 권한 조회가 200인데 JSON이 아니면(프록시 오류 페이지 등) 권한을 빠뜨린 채 성공하지 않고 실패한다.
func TestWhoamiPermissionsNonJSONBody(t *testing.T) {
	f := setup(t)
	f.myPermsHTML = true
	out, err := run(t, f, "whoami", "--permissions")
	if err == nil || !strings.Contains(err.Error(), "text/html") {
		t.Fatalf("Content-Type을 담은 오류를 기대했는데 %v", err)
	}
	if out != "" {
		t.Fatalf("실패하면 아무것도 출력하지 않아야 하는데: %s", out)
	}
}

// --permissions 없는 whoami는 권한 API를 부르지 않고, JSON에도 permissions가 없다.
func TestWhoamiWithoutPermissionsSkipsLookup(t *testing.T) {
	f := setup(t)
	out, err := run(t, f, "--json", "whoami")
	if err != nil {
		t.Fatal(err)
	}
	if n := len(f.seen("GET", "/api/authz/me/permissions")); n != 0 {
		t.Fatalf("권한 API를 부르지 않아야 하는데 %d번 불렀습니다", n)
	}
	if strings.Contains(out, `"permissions"`) {
		t.Fatalf("permissions 키가 없어야 하는데: %s", out)
	}
}
