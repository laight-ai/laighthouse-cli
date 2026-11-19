package cli

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func lastInviteBody(t *testing.T, f *fakeBackend) map[string]any {
	t.Helper()
	reqs := f.seen("POST", "/api/authz/invites")
	if len(reqs) == 0 {
		t.Fatal("초대 요청을 보내지 않았습니다")
	}
	var body map[string]any
	if err := json.Unmarshal(reqs[len(reqs)-1].body, &body); err != nil {
		t.Fatal(err)
	}
	return body
}

// 서버는 외부 역할 초대에 접근 만료일을 요구하므로, 없으면 요청 전에 막는다.
func TestInviteExternalRoleNeedsExpires(t *testing.T) {
	f := setup(t)
	_, err := run(t, f, "invite", "partner@y.com", "--role", "대행사")
	if err == nil || !strings.Contains(err.Error(), "--expires") {
		t.Fatalf("--expires 안내를 기대했는데 %v", err)
	}
	yesterday := time.Now().AddDate(0, 0, -1).Format(time.DateOnly)
	if _, err := run(t, f, "invite", "partner@y.com", "--role", "대행사", "--expires", yesterday); err == nil || !strings.Contains(err.Error(), "이미 지났습니다") {
		t.Fatalf("지난 날짜 오류를 기대했는데 %v", err)
	}
	if n := len(f.seen("POST", "/api/authz/invites")); n != 0 {
		t.Fatalf("요청을 보내지 않아야 하는데 %d번 보냄", n)
	}
}

// --expires는 그날 23:59:59(로컬)까지이고, 초대 링크는 그날을 넘기지 않게 줄어든다.
func TestInviteExpiresSetsBindingAndCapsLink(t *testing.T) {
	f := setup(t)
	day := time.Now().AddDate(0, 0, 3).Format(time.DateOnly)
	if _, err := run(t, f, "invite", "partner@y.com", "--role", "대행사", "--expires", day, "--ttl", "7d"); err != nil {
		t.Fatal(err)
	}
	body := lastInviteBody(t, f)
	got, err := time.Parse(time.RFC3339, body["binding_expires_at"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if local := got.In(time.Local); local.Format(time.DateTime) != day+" 23:59:59" {
		t.Fatalf("binding_expires_at이 로컬 %s 23:59:59여야 하는데 %s", day, local)
	}
	// CLI는 요청 전에 남은 시간을 재서 초 단위로 내리고, 테스트는 그보다 조금 뒤에 잰다. 1초 여유를 둔다.
	ttl := body["token_ttl_seconds"].(float64)
	if until := time.Until(got).Seconds(); ttl > until+1 || ttl < until-60 {
		t.Fatalf("초대 링크가 접근 만료일(%.0f초 뒤)까지로 줄어야 하는데 %.0f초", until, ttl)
	}

	// 만료일이 링크 기간보다 멀면 링크 기간은 그대로다.
	far := time.Now().AddDate(0, 0, 20).Format(time.DateOnly)
	if _, err := run(t, f, "invite", "partner@y.com", "--role", "대행사", "--expires", far, "--ttl", "7d"); err != nil {
		t.Fatal(err)
	}
	if ttl := lastInviteBody(t, f)["token_ttl_seconds"]; ttl != float64(7*24*3600) {
		t.Fatalf("링크 기간은 7일 그대로여야 하는데 %v", ttl)
	}
}

// 승인자 지정이 필요한 정책이면 후보를 보여 주고, 나머지 주소는 보내지 않는다.
func TestInviteApproverRequiredShowsCandidates(t *testing.T) {
	f := setup(t)
	f.needApprover = true
	_, err := run(t, f, "invite", "a@y.com", "b@y.com", "--role", "마케터")
	if err == nil || !strings.Contains(err.Error(), "lee@x.com") || !strings.Contains(err.Error(), "--approver") {
		t.Fatalf("후보와 --approver 안내를 기대했는데 %v", err)
	}
	if n := len(f.seen("POST", "/api/authz/invites")); n != 1 {
		t.Fatalf("첫 주소에서 멈춰야 하는데 %d번 보냄", n)
	}

	out, err := run(t, f, "--json", "invite", "a@y.com", "b@y.com", "--role", "마케터")
	if err == nil {
		t.Fatal("승인자가 빠졌으면 실패해야 합니다")
	}
	var results []struct {
		Email, Status       string
		ApproverDesignation *struct{ Candidates []struct{ Email string } } `json:"approver_designation"`
	}
	if err := json.Unmarshal([]byte(out), &results); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if len(results) != 2 || results[0].ApproverDesignation == nil || results[0].ApproverDesignation.Candidates[0].Email != "lee@x.com" ||
		results[1].Email != "b@y.com" || results[1].Status != "skipped" {
		t.Fatalf("--json 결과에 후보와, 보내지 않은 주소(skipped)가 있어야 하는데: %s", out)
	}
}

// 고른 승인자가 승인할 수 없으면(400 approver_designation) 나머지 주소도 같은 이유로 거절되므로 멈춘다.
func TestInviteBadApproverStops(t *testing.T) {
	f := setup(t)
	f.badApprover = true
	_, err := run(t, f, "invite", "a@y.com", "b@y.com", "--role", "마케터", "--approver", "u7")
	if err == nil || !strings.Contains(err.Error(), "2건 중 2건") {
		t.Fatalf("두 주소 모두 보내지 못했다는 오류를 기대했는데 %v", err)
	}
	if n := len(f.seen("POST", "/api/authz/invites")); n != 1 {
		t.Fatalf("첫 주소에서 멈춰야 하는데 %d번 보냄", n)
	}
}

// 이메일은 멤버 목록(두 번째 페이지 포함)에서 정확히 같은 주소로 찾고, @가 없으면 UID로 그대로 보낸다.
func TestInviteApproverResolvesEmail(t *testing.T) {
	f := setup(t)
	f.needApprover = true
	if _, err := run(t, f, "invite", "a@y.com", "--role", "마케터", "--approver", "LEE@x.com", "--approver", "u7"); err != nil {
		t.Fatal(err)
	}
	approval, _ := lastInviteBody(t, f)["approval"].(map[string]any)
	if want := []any{"u2", "u7"}; !reflect.DeepEqual(approval["approver_uids"], want) {
		t.Fatalf("approver_uids가 %v여야 하는데 %v", want, approval)
	}

	sent := len(f.seen("POST", "/api/authz/invites"))
	_, err := run(t, f, "invite", "a@y.com", "--role", "마케터", "--approver", "lee@")
	if err == nil || !strings.Contains(err.Error(), "UID를 쓰세요") {
		t.Fatalf("일부만 맞는 이메일은 받지 않고 UID 지정을 안내해야 하는데 %v", err)
	}
	if len(f.seen("POST", "/api/authz/invites")) != sent {
		t.Fatal("승인자를 못 찾으면 요청을 보내지 않아야 합니다")
	}
}
