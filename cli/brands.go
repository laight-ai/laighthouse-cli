package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/laight-ai/laighthouse-cli/internal/api"
	"github.com/laight-ai/laighthouse-cli/internal/config"
)

func newBrandsCmd(g *Env) *cobra.Command {
	return NounCmd(func() *cobra.Command { return newBrandsListCmd(g) },
		"brands", []string{"brand"},
		"브랜드 목록, 기본 브랜드 지정, 조직 트리",
		`내 계정이 접근할 수 있는 브랜드(광고주)를 봅니다. 웹의 브랜드 선택과 같은 목록입니다.
GET /api/resources

보고서·업로드 명령은 브랜드가 필요합니다. '`+binName+` brands use <이름>'으로 기본 브랜드를
정해 두면 매번 --brand를 붙이지 않아도 됩니다. 목록의 * 표시가 기본 브랜드입니다.`,
		newBrandsUseCmd(g), newBrandsTreeCmd(g), newBrandsAccessCmd(g))
}

func newBrandsListCmd(g *Env) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "브랜드 목록",
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := g.apiClient()
			if err != nil {
				return err
			}
			resp, err := c.ListBrandWithResponse(cmd.Context())
			if err != nil {
				return err
			}
			if err := CheckStatus(resp.HTTPResponse, resp.Body); err != nil {
				return err
			}
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			current, _ := cfg.DefaultBrandIn(g.scopeName(), g.env)
			rows := make([][]string, 0, len(*resp.JSON200))
			for _, b := range *resp.JSON200 {
				feats := make([]string, 0, len(b.EnabledFeatures))
				for _, f := range b.EnabledFeatures {
					feats = append(feats, string(f))
				}
				mark := ""
				if b.Uid == current.ID {
					mark = "*"
				}
				rows = append(rows, []string{mark, b.Uid, b.Name, string(b.BrandType), string(b.Country), strings.Join(feats, ",")})
			}
			return g.out.Table(resp.JSON200, []string{"", "ID", "이름", "유형", "국가", "기능"}, rows)
		},
	}
}

func newBrandsUseCmd(g *Env) *cobra.Command {
	return &cobra.Command{
		Use:   "use [이름|ID]",
		Short: "기본 브랜드 지정 (인자 없이 치면 현재 값 표시)",
		Long: `보고서·업로드 명령이 --brand 없이 쓸 기본 브랜드를 정합니다. 환경(dev/prod)마다 따로 저장됩니다.
이름은 일부만 적어도 하나로 정해지면 됩니다. 인자 없이 치면 지금 기본 브랜드를 보여 줍니다.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			w := cmd.OutOrStdout()
			if len(args) == 0 {
				b, ok := cfg.DefaultBrandIn(g.scopeName(), g.env)
				if g.json {
					if !ok {
						return g.out.JSONValue(nil)
					}
					return g.out.JSONValue(b)
				}
				if !ok {
					fmt.Fprintf(w, "%s 환경에 기본 브랜드가 없습니다. '"+binName+" brands use <이름>'으로 정하세요.\n", g.env)
					return nil
				}
				fmt.Fprintf(w, "%s [%s]\n", b.Name, b.ID)
				return nil
			}

			c, err := g.apiClient()
			if err != nil {
				return err
			}
			b, err := findBrand(cmd.Context(), c, args[0])
			if err != nil {
				return err
			}
			chosen := config.Brand{ID: b.Uid, Name: b.Name}
			cfg.SetDefaultBrandIn(g.scopeName(), g.env, chosen)
			if err := cfg.Save(); err != nil {
				return err
			}
			if g.json {
				return g.out.JSONValue(chosen)
			}
			fmt.Fprintf(w, "%s 환경의 기본 브랜드를 %s [%s](으)로 정했습니다.\n", g.env, b.Name, b.Uid)
			return nil
		},
	}
}

func newBrandsTreeCmd(g *Env) *cobra.Command {
	var showArchived bool
	cmd := &cobra.Command{
		Use:   "tree",
		Short: "조직의 브랜드 트리 (GET /api/authz/resources)",
		Long: `조직의 리소스 트리를 들여쓰기로 보여 줍니다. 웹의 "광고주" 화면과 같습니다.
잎 노드가 브랜드이며, 레벨 이름(예: 그룹 > 사업부 > 브랜드)은 조직마다 다릅니다.`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := g.apiClient()
			if err != nil {
				return err
			}
			resp, err := c.ListResourcesWithResponse(cmd.Context())
			if err != nil {
				return err
			}
			if err := CheckStatus(resp.HTTPResponse, resp.Body); err != nil {
				return err
			}
			if g.json {
				return g.out.JSONValue(resp.JSON200)
			}
			out := resp.JSON200
			w := cmd.OutOrStdout()
			if len(out.Levels) > 0 {
				names := make([]string, 0, len(out.Levels))
				for _, l := range out.Levels {
					names = append(names, l.Name)
				}
				fmt.Fprintf(w, "레벨: %s\n", strings.Join(names, " > "))
			}
			children := map[string][]api.ResourceOutput{}
			for _, r := range out.Items {
				if !showArchived && r.Status == api.ResourceStatusArchived {
					continue
				}
				children[Deref(r.ParentId)] = append(children[Deref(r.ParentId)], r)
			}
			var walk func(parent string, indent string)
			walk = func(parent string, indent string) {
				for _, r := range children[parent] {
					mark := ""
					if r.Status == api.ResourceStatusArchived {
						mark = " (보관됨)"
					}
					fmt.Fprintf(w, "%s%s  [%s]%s\n", indent, r.Name, r.Id, mark)
					walk(r.Id, indent+"  ")
				}
			}
			walk("", "")
			return nil
		},
	}
	cmd.Flags().BoolVar(&showArchived, "archived", false, "보관된 노드도 표시")
	return cmd
}

func newBrandsAccessCmd(g *Env) *cobra.Command {
	var cf CursorFlags
	cmd := &cobra.Command{
		Use:   "access [이름|ID]",
		Short: "브랜드에 접근할 수 있는 사람과 팀",
		Long: `브랜드(또는 'brands tree'의 그룹 노드)에 닿는 역할 부여를 봅니다. 조직 전체에 준 역할,
그 노드에 준 역할, 위 노드에 준 역할이 모두 나옵니다. 정지된 멤버와 만료된 게스트는 빠집니다.
인자가 없으면 --brand와 같은 순서(LAIGHTHOUSE_BRAND, 'brands use'로 정한 기본 브랜드)로 정합니다.
이름으로 고를 때 보관된 노드는 빠지고, 같은 이름이 여럿이면 ID로 지정해야 합니다.
대상·역할·범위는 이름으로 보여 주며, 내 권한 범위의 멤버 목록에 없는 대상은 UID로 나옵니다.
GET /api/authz/resources/{resource_id}/access
필요 권한: member.read`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := g.apiClient()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			// 트리는 이름으로 노드를 고를 때와 표의 범위 칸에만 쓴다.
			var tree []api.ResourceOutput
			fetchTree := func() error {
				resp, err := c.ListResourcesWithResponse(ctx)
				if err != nil {
					return err
				}
				if err := CheckStatus(resp.HTTPResponse, resp.Body); err != nil {
					return err
				}
				tree = resp.JSON200.Items
				return nil
			}
			var nodeID string
			if len(args) == 1 {
				if err := fetchTree(); err != nil {
					return err
				}
				if nodeID, err = pickNode(tree, args[0]); err != nil {
					return err
				}
			} else if nodeID, err = g.brand(ctx, c, ""); err != nil {
				return err
			}

			items, next, err := Collect(cf, func(after *string, limit int) ([]api.BindingOutput, *string, error) {
				resp, err := c.ListNodeAccessWithResponse(ctx, nodeID, &api.ListNodeAccessParams{After: after, Limit: &limit})
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
			value := map[string]any{"items": items, "next_after": next}
			if g.json {
				return g.out.JSONValue(value)
			}

			names := map[string]string{}
			if len(items) > 0 {
				if tree == nil {
					if err := fetchTree(); err != nil {
						return err
					}
				}
				if names, err = accessNames(ctx, c, tree); err != nil {
					return err
				}
			}
			label := func(id string) string {
				if n, ok := names[id]; ok {
					return n
				}
				return id
			}
			rows := make([][]string, 0, len(items))
			for _, b := range items {
				kind := "사람"
				if b.SubjectType == api.Team {
					kind = "팀"
				}
				scope := "조직 전체"
				if b.ScopeType == api.BindingScopeTypeResource {
					scope = label(Deref(b.ScopeResourceId))
				}
				exp := ""
				if b.ExpiresAt != nil {
					exp = b.ExpiresAt.Format(time.DateOnly)
				}
				rows = append(rows, []string{label(b.SubjectId), kind, label(b.RoleId), scope, exp, b.GrantedAt.Format(time.DateOnly)})
			}
			return g.out.Table(value, []string{"대상", "구분", "역할", "범위", "만료일", "부여일"}, rows)
		},
	}
	cf.Bind(cmd)
	return cmd
}

// pickNode는 ID나 이름으로 트리 노드를 고른다. ID는 보관된 노드도 받지만, 이름으로는
// 'brands tree'에 보이는 활성 노드만 고른다.
func pickNode(tree []api.ResourceOutput, q string) (string, error) {
	active := make([]api.ResourceOutput, 0, len(tree))
	for _, r := range tree {
		if r.Id == q {
			return r.Id, nil
		}
		if r.Status != api.ResourceStatusArchived {
			active = append(active, r)
		}
	}
	n, err := Pick("노드", q, active,
		func(r api.ResourceOutput) string { return r.Id },
		func(r api.ResourceOutput) string { return r.Name })
	return n.Id, err
}

// accessNames는 멤버 UID, 팀·역할·노드 ID를 사람이 읽는 이름으로 바꾸는 표를 만든다.
// 세 목록 모두 접근 목록과 같은 member.read로 열리므로 실패는 그대로 돌려준다.
// 노드 범위로 member.read를 가진 사람은 멤버 목록이 그 범위로 줄어, 목록에 없는 대상은 UID로 남는다.
func accessNames(ctx context.Context, c *api.ClientWithResponses, tree []api.ResourceOutput) (map[string]string, error) {
	all := CursorFlags{All: true, Limit: 200}
	names := map[string]string{}
	for _, r := range tree {
		names[r.Id] = r.Name
	}
	members, _, err := listMembers(ctx, c, all)
	if err != nil {
		return nil, err
	}
	for _, m := range members {
		if e := Deref(m.Email); e != "" {
			names[m.UserUid] = e
		}
	}
	teams, _, err := listTeams(ctx, c, all)
	if err != nil {
		return nil, err
	}
	for _, t := range teams {
		names[t.Id] = t.Name
	}
	roles, _, err := listRoles(ctx, c, all)
	if err != nil {
		return nil, err
	}
	for _, r := range roles {
		names[r.Id] = r.Name
	}
	return names, nil
}
