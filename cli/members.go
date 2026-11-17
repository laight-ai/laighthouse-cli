package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/laight-ai/laighthouse-cli/internal/api"
)

func newMembersCmd(g *Env) *cobra.Command {
	return NounCmd(func() *cobra.Command { return newMembersListCmd(g) },
		"members", []string{"member"},
		"조직 멤버 목록과 멤버별 권한",
		`내 조직(테넌트)의 멤버 명단을 봅니다. 웹의 "조직 관리 > 멤버" 화면에 해당합니다.
GET /api/authz/members

정식 멤버(member)와 게스트(guest)가 함께 나오며, 게스트는 만료일이 표시됩니다.
새 멤버를 부르려면 '`+binName+` invite <이메일>'을 쓰세요.
필요 권한: member.read (조직 전체)`,
		newMembersShowCmd(g))
}

func newMembersListCmd(g *Env) *cobra.Command {
	var cf CursorFlags
	cmd := &cobra.Command{
		Use:   "list",
		Short: "멤버 목록",
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := g.apiClient()
			if err != nil {
				return err
			}
			items, next, err := listMembers(cmd.Context(), c, cf)
			if err != nil {
				return err
			}
			rows := make([][]string, 0, len(items))
			for _, m := range items {
				exp := ""
				if m.ExpiresAt != nil {
					exp = m.ExpiresAt.Format(time.DateOnly)
				}
				rows = append(rows, []string{Deref(m.Email), Deref(m.Name), string(m.Kind), m.Status, m.JoinedAt.Format(time.DateOnly), exp, m.UserUid})
			}
			return g.out.Table(map[string]any{"items": items, "next_after": next},
				[]string{"이메일", "이름", "구분", "상태", "가입일", "만료일", "UID"}, rows)
		},
	}
	cf.Bind(cmd)
	return cmd
}

func listMembers(ctx context.Context, c *api.ClientWithResponses, cf CursorFlags) ([]api.MemberOutput, *string, error) {
	return Collect(cf, func(after *string, limit int) ([]api.MemberOutput, *string, error) {
		resp, err := c.ListMembersWithResponse(ctx, &api.ListMembersParams{After: after, Limit: &limit})
		if err != nil {
			return nil, nil, err
		}
		if err := CheckStatus(resp.HTTPResponse, resp.Body); err != nil {
			return nil, nil, err
		}
		return resp.JSON200.Items, resp.JSON200.NextAfter, nil
	})
}

func newMembersShowCmd(g *Env) *cobra.Command {
	return &cobra.Command{
		Use:     "show <이메일|UID>",
		Aliases: []string{"get", "view"},
		Short:   "멤버 정보와 가진 권한",
		Long: `멤버 한 명의 정보와, 그 멤버가 어떤 권한을 어느 범위(조직 전체 또는 브랜드)에서 가졌는지 봅니다.
웹의 "이 사용자로 보기"와 같습니다. 이메일은 일부만 적어도 한 명으로 정해지면 됩니다.
GET /api/authz/members/{uid}/permissions
필요 권한: member.read, permission.inspect`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := g.apiClient()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			members, _, err := listMembers(ctx, c, CursorFlags{All: true, Limit: 200})
			if err != nil {
				return err
			}
			m, err := Pick("멤버", args[0], members,
				func(m api.MemberOutput) string { return m.UserUid },
				func(m api.MemberOutput) string { return Deref(m.Email) })
			if err != nil {
				return err
			}
			resp, err := c.GetMemberHeldPermissionsWithResponse(ctx, m.UserUid)
			if err != nil {
				return err
			}
			if err := CheckStatus(resp.HTTPResponse, resp.Body); err != nil {
				return err
			}
			if g.json {
				return g.out.JSONValue(map[string]any{"member": m, "permissions": resp.JSON200})
			}
			w := cmd.OutOrStdout()
			fmt.Fprintf(w, "이메일: %s\n이름:   %s\n구분:   %s\n상태:   %s\n가입일: %s\nUID:    %s\n",
				Deref(m.Email), Deref(m.Name), m.Kind, m.Status, m.JoinedAt.Format(time.DateOnly), m.UserUid)
			if m.ExpiresAt != nil {
				fmt.Fprintf(w, "만료일: %s\n", m.ExpiresAt.Format(time.DateOnly))
			}
			fmt.Fprintln(w, "\n권한:")
			return g.out.Table(nil, []string{"범위", "권한 키"}, heldScopeRows(resp.JSON200.Items))
		},
	}
}

// heldScopeRows는 가진 권한을 범위마다 한 줄(범위, 권한 키 목록)로 만든다.
func heldScopeRows(items []api.HeldScopeOutput) [][]string {
	rows := make([][]string, 0, len(items))
	for _, s := range items {
		scope := "조직 전체"
		if s.ScopeType == api.BindingScopeTypeResource {
			scope = "브랜드 " + Deref(s.ScopeResourceId)
		}
		keys := make([]string, 0, len(s.PermissionKeys))
		for _, k := range s.PermissionKeys {
			keys = append(keys, string(k))
		}
		rows = append(rows, []string{scope, strings.Join(keys, ", ")})
	}
	return rows
}
