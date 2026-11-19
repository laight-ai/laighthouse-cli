package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/laight-ai/laighthouse-cli/internal/api"
)

func newInviteCmd(g *Env) *cobra.Command {
	var (
		roleQ     string
		brands    []string
		ttlText   string
		replace   bool
		expText   string
		approvers []string
	)
	cmd := &cobra.Command{
		Use:   "invite <이메일>... --role <역할>",
		Short: "멤버 초대 보내기, 보낸 초대 관리",
		Long: `조직에 사람을 초대합니다. 웹의 "멤버 초대"와 같습니다. 서버가 초대 메일을 보내고,
받은 사람이 메일의 링크로 수락하면 역할이 부여됩니다. 이메일을 여러 개 적으면 한 명씩 보냅니다.
POST /api/authz/invites

--role에는 역할 이름이나 ID를 씁니다('` + binName + ` roles'로 확인).
--brand를 주면 그 브랜드로 범위가 제한되고, 없으면 조직 전체 범위입니다.
이미 초대한 주소면 실패합니다. 메일만 다시 보내려면 'invite resend <ID>',
조건을 바꿔 새로 보내려면 --replace를 붙이세요.
--expires는 초대받은 사람의 접근이 끝나는 날(그날 23:59:59까지)입니다. 외부 역할이면 꼭 정해야 하고,
초대 링크도 그날을 넘기지 않게 줄어듭니다.
결재 정책이 걸려 있으면 결재 대기가 되고, 승인되면 메일이 나갑니다. 승인자를 직접 골라야 하는
정책이면 --approver로 정합니다. 빠뜨리면 고를 수 있는 후보를 보여 줍니다.

보낸 초대는 'invite list'로 보고 'invite revoke <ID>'로 취소합니다.
필요 권한: member.invite`,
		Example: `  ` + binName + ` invite kim@example.com --role 마케터
  ` + binName + ` invite kim@example.com lee@example.com --role 마케터 --brand 브랜드A --ttl 3d
  ` + binName + ` invite partner@agency.com --role 대행사 --expires 2026-12-31 --approver boss@example.com`,
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return errors.New("초대할 이메일을 적으세요: " + binName + " invite <이메일> --role <역할>")
			}
			for _, a := range args {
				if !strings.Contains(a, "@") {
					return fmt.Errorf("%w. 이메일이라면 @가 들어가야 합니다", UnknownCommand(cmd, a))
				}
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if roleQ == "" {
				return errors.New("--role이 필요합니다. '" + binName + " roles'로 역할을 확인하세요")
			}
			ttl, err := parseTTL(ttlText)
			if err != nil {
				return err
			}
			if ttl < time.Hour || ttl > 30*24*time.Hour {
				return errors.New("--ttl은 1h 이상 30d 이하여야 합니다")
			}
			var expires *time.Time
			if expText != "" {
				day, err := time.ParseInLocation(time.DateOnly, expText, time.Local)
				if err != nil {
					return fmt.Errorf("--expires는 YYYY-MM-DD 형식이어야 합니다: %q", expText)
				}
				end := day.AddDate(0, 0, 1).Add(-time.Second)
				until := time.Until(end)
				if until <= 0 {
					return fmt.Errorf("--expires가 이미 지났습니다: %s", expText)
				}
				// 접근이 끝난 뒤에 남은 초대는 수락해도 실패하므로, 초대 링크도 그날을 넘기지 않게 줄인다(웹과 같음).
				if until < ttl {
					if until < time.Hour {
						return fmt.Errorf("--expires까지 1시간도 남지 않아 초대 링크를 만들 수 없습니다: %s", expText)
					}
					ttl = until
				}
				expires = &end
			}
			ctx := cmd.Context()
			c, err := g.apiClient()
			if err != nil {
				return err
			}
			roles, _, err := listRoles(ctx, c, CursorFlags{All: true, Limit: 200})
			if err != nil {
				return err
			}
			role, err := Pick("역할", roleQ, roles,
				func(r api.RoleOutput) string { return r.Id },
				func(r api.RoleOutput) string { return r.Name })
			if err != nil {
				return err
			}
			if role.IsExternal && expires == nil {
				return fmt.Errorf("%s는 외부 역할이라 접근 만료일이 필요합니다. --expires <YYYY-MM-DD>를 붙이세요", role.Name)
			}
			base := api.PostInviteInput{
				RoleId:           role.Id,
				ScopeType:        api.BindingScopeTypeTenant,
				TokenTtlSeconds:  int(ttl.Seconds()),
				BindingExpiresAt: expires,
			}
			if len(approvers) > 0 {
				uids, err := resolveApprovers(ctx, c, approvers)
				if err != nil {
					return err
				}
				base.Approval = &api.ApprovalFilingInput{ApproverUids: uids}
			}
			if len(brands) > 0 {
				ids := make([]string, 0, len(brands))
				for _, q := range brands {
					b, err := findBrand(ctx, c, q)
					if err != nil {
						return err
					}
					ids = append(ids, b.Uid)
				}
				base.ScopeType = api.BindingScopeTypeResource
				base.ScopeResourceIds = &ids
			}

			var results []inviteResult
			for i, email := range args {
				r := sendInvite(ctx, c, base, email, replace)
				results = append(results, r)
				// 승인자 문제는 결재 정책과 --approver에서 오므로 나머지 주소도 같은 이유로 거절된다.
				// 더 보내지 않고, 보내지 않은 주소도 결과에 남긴다.
				if r.approverProblem {
					for _, rest := range args[i+1:] {
						results = append(results, inviteResult{Email: rest, Status: "skipped"})
					}
					break
				}
			}
			if g.json {
				if err := g.out.JSONValue(results); err != nil {
					return err
				}
			} else {
				w := cmd.OutOrStdout()
				for _, r := range results {
					switch r.Status {
					case "sent":
						fmt.Fprintf(w, "%s: 초대 메일을 보냈습니다 (초대 ID %s, %s까지 유효, 역할 %s)\n",
							r.Email, r.Invite.Id, r.Invite.ExpiresAt.Local().Format(time.DateTime), role.Name)
					case "pending_approval":
						fmt.Fprintf(w, "%s: 결재 대기 중입니다. 승인되면 메일이 나갑니다 (결재 요청 %s)\n", r.Email, r.Approval.ApprovalRequestId)
					case "skipped":
						fmt.Fprintf(w, "%s: 보내지 않음 - 앞 주소와 같은 승인자 문제로 거절되므로 멈췄습니다\n", r.Email)
					default:
						fmt.Fprintf(w, "%s: 실패 - %s\n", r.Email, r.Error)
					}
				}
			}
			notSent := 0
			for _, r := range results {
				if r.Designation != nil {
					return approverCandidatesError(r.Designation)
				}
				if r.Status == "failed" || r.Status == "skipped" {
					notSent++
				}
			}
			if notSent > 0 {
				return fmt.Errorf("%d건 중 %d건을 보내지 못했습니다", len(results), notSent)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&roleQ, "role", "", "부여할 역할 이름 또는 ID (필수)")
	cmd.Flags().StringArrayVarP(&brands, "brand", "b", nil, "범위를 제한할 브랜드 이름 또는 ID (여러 번 지정 가능)")
	cmd.Flags().StringVar(&ttlText, "ttl", "7d", "초대 링크 유효 기간 (예: 12h, 7d. 1h ~ 30d)")
	cmd.Flags().BoolVar(&replace, "replace", false, "이미 초대한 주소면 기존 초대를 이번 조건으로 바꿔 다시 보냄")
	cmd.Flags().StringVar(&expText, "expires", "", "접근 만료일 YYYY-MM-DD (그날 23:59:59까지. 외부 역할이면 필수)")
	cmd.Flags().StringArrayVar(&approvers, "approver", nil, "결재 승인자 이메일 또는 UID (여러 번 지정 가능. 정책이 승인자 지정을 요구할 때)")
	cmd.AddCommand(newInviteListCmd(g), newInviteResendCmd(g), newInviteRevokeCmd(g))
	return cmd
}

// inviteResult는 이메일 하나에 초대를 보낸 결과다. --json 출력에도 그대로 쓴다.
type inviteResult struct {
	Email    string                     `json:"email"`
	Status   string                     `json:"status"` // sent | pending_approval | failed | skipped
	Invite   *api.PostInviteOutput      `json:"invite,omitempty"`
	Approval *api.ApprovalPendingOutput `json:"approval,omitempty"`
	Error    string                     `json:"error,omitempty"`
	// Designation은 결재 정책이 승인자 지정을 요구하는데 --approver가 없을 때 서버가 준 후보다.
	Designation *api.ApproverDesignationRequiredErrorResponse `json:"approver_designation,omitempty"`
	// approverProblem은 승인자 때문에 거절됐다는 뜻이다(409 지정 필요, 400 잘못 지정). 나머지 주소를 보내지 않는다.
	approverProblem bool
}

func sendInvite(ctx context.Context, c *api.ClientWithResponses, base api.PostInviteInput, email string, replace bool) inviteResult {
	r := inviteResult{Email: email, Status: "failed"}
	body := base
	body.EmailOrDomainRestriction = &email
	for {
		resp, err := c.CreateInviteWithResponse(ctx, body)
		if err != nil {
			r.Error = err.Error()
			return r
		}
		switch {
		case resp.JSON201 != nil:
			r.Status, r.Invite = "sent", resp.JSON201
			return r
		case resp.JSON202 != nil:
			r.Status, r.Approval = "pending_approval", resp.JSON202
			return r
		}
		conflict := inviteConflict(resp.HTTPResponse, resp.Body)
		if conflict != nil && conflict.Reason == api.AlreadyInvited && conflict.InviteId != nil {
			if replace && body.ReplacesInviteId == nil {
				body.ReplacesInviteId = conflict.InviteId
				continue
			}
			r.Error = fmt.Sprintf("이미 초대한 주소입니다 (초대 ID %s). 메일만 다시 보내려면 '"+binName+" invite resend %s', 새 조건으로 바꾸려면 --replace",
				*conflict.InviteId, *conflict.InviteId)
			return r
		}
		if d := approverDesignation(resp.HTTPResponse, resp.Body); d != nil {
			r.Error, r.Designation, r.approverProblem = "결재 승인자를 정해야 합니다", d, true
			return r
		}
		r.Error = CheckStatus(resp.HTTPResponse, resp.Body).Error()
		// 400 approver_designation: 고른 승인자가 승인할 수 없거나, 정책이 승인자 지정을 받지 않는다.
		var bad api.ApproverDesignationErrorResponse
		if resp.StatusCode() == http.StatusBadRequest && json.Unmarshal(resp.Body, &bad) == nil && bad.Reason == api.ApproverDesignation {
			r.approverProblem = true
		}
		return r
	}
}

// approverDesignation은 결재 정책이 승인자 지정을 요구하는데 빠졌다는 409(approver_designation_required)를
// 읽는다. 아니면 nil.
func approverDesignation(resp *http.Response, body []byte) *api.ApproverDesignationRequiredErrorResponse {
	if resp.StatusCode != http.StatusConflict {
		return nil
	}
	var e api.ApproverDesignationRequiredErrorResponse
	if json.Unmarshal(body, &e) != nil || e.Reason != api.ApproverDesignationRequired {
		return nil
	}
	return &e
}

// approverCandidatesError는 서버가 준 후보와 최소 인원으로 --approver를 안내하는 오류를 만든다.
func approverCandidatesError(e *api.ApproverDesignationRequiredErrorResponse) error {
	var b strings.Builder
	fmt.Fprintf(&b, "결재 승인자를 정해야 합니다. --approver로 다음 중 %d명 이상을 골라 다시 실행하세요(필요한 승인 %d명):", e.MinDesignated, e.RequiredApprovals)
	for _, c := range e.Candidates {
		fmt.Fprintf(&b, "\n  %s %s [%s]", Deref(c.Email), Deref(c.Name), c.Uid)
	}
	return errors.New(b.String())
}

// resolveApprovers는 --approver 값을 UID로 바꾼다. @가 있으면 멤버 목록에서 이메일이 정확히 같은 사람을
// 찾고(승인자를 잘못 고르지 않게 일부 일치는 받지 않는다), 없으면 UID로 본다.
func resolveApprovers(ctx context.Context, c *api.ClientWithResponses, qs []string) ([]string, error) {
	var members []api.MemberOutput
	uids := make([]string, 0, len(qs))
	for _, q := range qs {
		if !strings.Contains(q, "@") {
			uids = append(uids, q)
			continue
		}
		if members == nil {
			var err error
			if members, _, err = listMembers(ctx, c, CursorFlags{All: true, Limit: 200}); err != nil {
				return nil, err
			}
		}
		uid := ""
		for _, m := range members {
			if strings.EqualFold(Deref(m.Email), q) {
				uid = m.UserUid
				break
			}
		}
		if uid == "" {
			return nil, fmt.Errorf("승인자 %s를 멤버에서 찾지 못했습니다. --approver 없이 실행하면 고를 수 있는 후보를 보여 줍니다", q)
		}
		uids = append(uids, uid)
	}
	return uids, nil
}

// inviteConflict는 초대 발급·재발송이 기존 초대 때문에 거절된 응답(409/429)을 읽는다. 아니면 nil.
func inviteConflict(resp *http.Response, body []byte) *api.InviteConflictErrorResponse {
	if resp.StatusCode != http.StatusConflict && resp.StatusCode != http.StatusTooManyRequests {
		return nil
	}
	var e api.InviteConflictErrorResponse
	if json.Unmarshal(body, &e) != nil || e.Reason == "" {
		return nil
	}
	return &e
}

func newInviteListCmd(g *Env) *cobra.Command {
	var cf CursorFlags
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "보낸 초대 목록 (GET /api/authz/invites)",
		Long:    `보낸 초대와 메일 발송 상태(queued, sent, failed, skipped)를 봅니다.`,
		Args:    noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			c, err := g.apiClient()
			if err != nil {
				return err
			}
			items, next, err := Collect(cf, func(after *string, limit int) ([]api.InviteOutput, *string, error) {
				resp, err := c.ListInvitesWithResponse(ctx, &api.ListInvitesParams{After: after, Limit: &limit})
				if err != nil {
					return nil, nil, err
				}
				if err := CheckStatus(resp.HTTPResponse, resp.Body); err != nil {
					return nil, nil, err
				}
				return resp.JSON200.Items, resp.JSON200.NextAfter, nil
			})
			if err != nil {
				return err
			}
			if g.json {
				return g.out.JSONValue(map[string]any{"items": items, "next_after": next})
			}
			// 역할 ID 대신 이름을 보여 주려고 역할 목록을 한 번 더 부른다. 실패해도 ID로 보여 준다.
			roleNames := map[string]string{}
			if roles, _, err := listRoles(ctx, c, CursorFlags{All: true, Limit: 200}); err == nil {
				for _, r := range roles {
					roleNames[r.Id] = r.Name
				}
			}
			rows := make([][]string, 0, len(items))
			for _, in := range items {
				role := roleNames[in.RoleId]
				if role == "" {
					role = in.RoleId
				}
				scope := "조직 전체"
				if len(in.ScopeResourceIds) > 0 {
					scope = strings.Join(in.ScopeResourceIds, ",")
				}
				mail := ""
				if in.LastMailStatus != nil {
					mail = string(*in.LastMailStatus)
				}
				rows = append(rows, []string{Deref(in.EmailOrDomainRestriction), string(in.Status), role, scope, mail,
					in.ExpiresAt.Local().Format(time.DateTime), in.Id})
			}
			return g.out.Table(nil, []string{"이메일", "상태", "역할", "범위", "메일", "만료", "ID"}, rows)
		},
	}
	cf.Bind(cmd)
	return cmd
}

func newInviteResendCmd(g *Env) *cobra.Command {
	return &cobra.Command{
		Use:   "resend <초대 ID>",
		Short: "초대 메일 다시 보내기 (POST /api/authz/invites/{id}/resend)",
		Long: `초대 조건은 그대로 두고 메일만 다시 보냅니다. 너무 자주 보내면 잠시 뒤에 다시 하라는 안내가 나옵니다.
이미 수락됐거나 결재 대기 중인 초대, 이미 멤버인 주소에는 보낼 수 없습니다.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := g.apiClient()
			if err != nil {
				return err
			}
			resp, err := c.ResendInviteWithResponse(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if err := CheckStatus(resp.HTTPResponse, resp.Body); err != nil {
				return err
			}
			if g.json {
				return g.out.JSONValue(resp.JSON200)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s에 초대 메일을 다시 보냈습니다.\n", Deref(resp.JSON200.EmailOrDomainRestriction))
			return nil
		},
	}
}

func newInviteRevokeCmd(g *Env) *cobra.Command {
	return &cobra.Command{
		Use:   "revoke <초대 ID>",
		Short: "초대 취소 (DELETE /api/authz/invites/{id})",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := g.apiClient()
			if err != nil {
				return err
			}
			resp, err := c.DeleteInviteWithResponse(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if err := CheckStatus(resp.HTTPResponse, resp.Body); err != nil {
				return err
			}
			if g.json {
				return g.out.JSONValue(map[string]any{"id": args[0], "revoked": true})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "초대 %s를 취소했습니다.\n", args[0])
			return nil
		},
	}
}
