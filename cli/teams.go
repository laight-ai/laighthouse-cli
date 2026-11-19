package cli

import (
	"context"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/laight-ai/laighthouse-cli/internal/api"
)

func newTeamsCmd(g *Env) *cobra.Command {
	return NounCmd(func() *cobra.Command { return newTeamsListCmd(g) },
		"teams", []string{"team"},
		"조직의 팀 목록",
		`멤버를 묶는 팀과 인원 수를 봅니다. 웹의 "조직 관리 > 팀"과 같습니다.
GET /api/authz/teams
팀 만들기·멤버 추가는 '`+binName+` api'로 부를 수 있습니다.`)
}

func newTeamsListCmd(g *Env) *cobra.Command {
	var cf CursorFlags
	cmd := &cobra.Command{
		Use:   "list",
		Short: "팀 목록",
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := g.apiClient()
			if err != nil {
				return err
			}
			items, next, err := listTeams(cmd.Context(), c, cf)
			if err != nil {
				return err
			}
			rows := make([][]string, 0, len(items))
			for _, t := range items {
				rows = append(rows, []string{t.Name, strconv.Itoa(t.MemberCount), t.Id})
			}
			return g.out.Table(map[string]any{"items": items, "next_after": next}, []string{"이름", "인원", "ID"}, rows)
		},
	}
	cf.Bind(cmd)
	return cmd
}

func listTeams(ctx context.Context, c *api.ClientWithResponses, cf CursorFlags) ([]api.TeamOutput, *string, error) {
	return Collect(cf, func(after *string, limit int) ([]api.TeamOutput, *string, error) {
		resp, err := c.ListTeamsWithResponse(ctx, &api.ListTeamsParams{After: after, Limit: &limit})
		if err != nil {
			return nil, nil, err
		}
		if err := CheckStatus(resp.HTTPResponse, resp.Body); err != nil {
			return nil, nil, err
		}
		return resp.JSON200.Items, resp.JSON200.NextAfter, nil
	})
}

func newRolesCmd(g *Env) *cobra.Command {
	return NounCmd(func() *cobra.Command { return newRolesListCmd(g) },
		"roles", []string{"role"},
		"조직의 역할과 권한 키",
		`역할과 그 역할이 가진 권한 키를 봅니다. "Owner"는 모든 권한을 가진 기본 역할입니다.
GET /api/authz/roles
'`+binName+` invite --role'에는 여기 나온 이름 전체나 ID를 씁니다.
역할은 'roles create'로 만들고 'roles update'·'roles delete'로 바꾸거나 지웁니다.`,
		newRolesShowCmd(g), newRolesCreateCmd(g), newRolesUpdateCmd(g), newRolesDeleteCmd(g))
}

func newRolesListCmd(g *Env) *cobra.Command {
	var cf CursorFlags
	cmd := &cobra.Command{
		Use:   "list",
		Short: "역할 목록",
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := g.apiClient()
			if err != nil {
				return err
			}
			items, next, err := listRoles(cmd.Context(), c, cf)
			if err != nil {
				return err
			}
			rows := make([][]string, 0, len(items))
			for _, r := range items {
				keys := make([]string, 0, len(r.PermissionKeys))
				for _, k := range r.PermissionKeys {
					keys = append(keys, string(k))
				}
				owner := ""
				if r.IsOwner {
					owner = "owner"
				}
				rows = append(rows, []string{r.Name, owner, strconv.Itoa(len(keys)), strings.Join(keys, ","), r.Id})
			}
			return g.out.Table(map[string]any{"items": items, "next_after": next}, []string{"이름", "", "권한 수", "권한 키", "ID"}, rows)
		},
	}
	cf.Bind(cmd)
	return cmd
}

func listRoles(ctx context.Context, c *api.ClientWithResponses, cf CursorFlags) ([]api.RoleOutput, *string, error) {
	return Collect(cf, func(after *string, limit int) ([]api.RoleOutput, *string, error) {
		resp, err := c.ListRolesWithResponse(ctx, &api.ListRolesParams{After: after, Limit: &limit})
		if err != nil {
			return nil, nil, err
		}
		if err := CheckStatus(resp.HTTPResponse, resp.Body); err != nil {
			return nil, nil, err
		}
		return resp.JSON200.Items, resp.JSON200.NextAfter, nil
	})
}
