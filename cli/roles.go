package cli

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/laight-ai/laighthouse-cli/internal/api"
)

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

// writeRequest는 쓰기 명령이 보낼 요청이다. --dry-run이면 보내지 않고 이것만 보여 준다.
type writeRequest struct {
	summary      string
	method, path string
	body         any // nil이면 본문 없음
}

// printDryRun은 보낼 요청을 stdout에 쓴다. --json이면 {"method","path","body"} JSON 하나로 쓴다.
func (g *Env) printDryRun(cmd *cobra.Command, r writeRequest) error {
	if g.json {
		return g.out.JSONValue(map[string]any{"method": r.method, "path": r.path, "body": r.body})
	}
	fmt.Fprintf(cmd.OutOrStdout(), "%s\n%s %s\n", r.summary, r.method, r.path)
	if r.body == nil {
		return nil
	}
	return g.out.JSONValue(r.body)
}

// rolePermFlags는 역할에 줄 권한 키를 받는 --permission·--all-permissions 플래그다. 목록 명령의 --all(끝까지
// 가져옴)과 헷갈리지 않게 이름을 따로 둔다.
type rolePermFlags struct {
	keys []string
	all  bool
}

func (f *rolePermFlags) bind(cmd *cobra.Command) {
	cmd.Flags().StringSliceVarP(&f.keys, "permission", "p", nil, "줄 권한 키 (쉼표로 잇거나 여러 번 지정)")
	cmd.Flags().BoolVar(&f.all, "all-permissions", false, "내가 조직 전체에서 가진 권한 키 중 이 역할에 줄 수 있는 것을 모두 줌")
	cmd.MarkFlagsMutuallyExclusive("permission", "all-permissions")
}

func (f *rolePermFlags) set() bool { return f.all || len(f.keys) > 0 }

// resolve는 줄 권한 키 목록을 돌려준다. --all-permissions면 서버의 권한 목록(이 조직이 가질 수 있는 키) 가운데
// 내가 조직 전체에서 가진 키만 고른다. 서버는 역할을 만들거나 바꾸는 사람이 새 키를 모두 조직 전체에서
// 가졌는지 보기 때문이다. 외부 역할이면 외부 역할에 줄 수 있는 키만 고른다.
func (f *rolePermFlags) resolve(ctx context.Context, c *api.ClientWithResponses, external bool) ([]string, error) {
	if !f.all {
		return f.keys, nil
	}
	resp, err := c.ListPermissionRegistryWithResponse(ctx)
	if err != nil {
		return nil, err
	}
	if err := CheckStatus(resp.HTTPResponse, resp.Body); err != nil {
		return nil, err
	}
	if resp.JSON200 == nil {
		return nil, fmt.Errorf("권한 목록 응답을 JSON으로 읽지 못했습니다 (Content-Type: %s)", resp.HTTPResponse.Header.Get("Content-Type"))
	}
	mine, err := c.GetMyHeldPermissionsWithResponse(ctx)
	if err != nil {
		return nil, err
	}
	if err := CheckStatus(mine.HTTPResponse, mine.Body); err != nil {
		return nil, err
	}
	if mine.JSON200 == nil {
		return nil, fmt.Errorf("내 권한 응답을 JSON으로 읽지 못했습니다 (Content-Type: %s)", mine.HTTPResponse.Header.Get("Content-Type"))
	}
	held := map[string]bool{}
	for _, scope := range mine.JSON200.Items {
		if scope.ScopeType != api.BindingScopeTypeTenant {
			continue
		}
		for _, k := range scope.PermissionKeys {
			held[string(k)] = true
		}
	}
	keys := make([]string, 0, len(resp.JSON200.Items))
	for _, p := range resp.JSON200.Items {
		if !held[string(p.Key)] || (external && !p.ExternalGrantable) {
			continue
		}
		keys = append(keys, string(p.Key))
	}
	// 빈 목록을 보내면 역할의 권한이 모두 지워지거나 권한 없는 역할이 생긴다.
	if len(keys) == 0 {
		if external {
			return nil, errors.New("--all-permissions로 줄 키가 없습니다. 내가 조직 전체에서 가진 키 가운데 외부 역할에 줄 수 있는 것이 없습니다")
		}
		return nil, errors.New("--all-permissions로 줄 키가 없습니다. 내가 조직 전체에서 가진 권한 키가 없습니다")
	}
	return keys, nil
}

func toKeys[T ~string](keys []string) []T {
	out := make([]T, len(keys))
	for i, k := range keys {
		out[i] = T(k)
	}
	return out
}

// findRole은 ID나 이름 전체(대소문자 무시)로 역할 하나를 고른다. 역할을 바꾸고 지우고 부여하는 데 쓰므로
// Pick과 달리 이름 일부만 맞는 역할은 고르지 않는다.
func findRole(ctx context.Context, c *api.ClientWithResponses, q string) (api.RoleOutput, error) {
	roles, _, err := listRoles(ctx, c, CursorFlags{All: true, Limit: 200})
	if err != nil {
		return api.RoleOutput{}, err
	}
	var exact []api.RoleOutput
	for _, r := range roles {
		if r.Id == q || strings.EqualFold(r.Name, q) {
			exact = append(exact, r)
		}
	}
	if len(exact) == 0 {
		names := make([]string, 0, len(roles))
		for _, r := range roles {
			names = append(names, r.Name)
		}
		return api.RoleOutput{}, fmt.Errorf("이름이나 ID가 %q인 역할이 없습니다. 이름 전체를 적으세요. 있는 역할: %s", q, strings.Join(names, ", "))
	}
	// 하나면 그것을, 이름이 같은 역할이 여럿이면 ID로 지정하라는 오류를 Pick이 돌려준다.
	return Pick("역할", q, exact,
		func(r api.RoleOutput) string { return r.Id },
		func(r api.RoleOutput) string { return r.Name })
}

func newRolesShowCmd(g *Env) *cobra.Command {
	return &cobra.Command{
		Use:     "show <역할>",
		Aliases: []string{"get", "view"},
		Short:   "역할 하나의 정보와 권한 키",
		Long: `역할 하나의 정보와 가진 권한 키를 한 줄에 하나씩 봅니다. 역할은 이름 전체나 ID로 지정합니다.
GET /api/authz/roles`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			c, err := g.apiClient()
			if err != nil {
				return err
			}
			r, err := findRole(ctx, c, args[0])
			if err != nil {
				return err
			}
			if g.json {
				return g.out.JSONValue(r)
			}
			yes := func(b bool) string {
				if b {
					return "예"
				}
				return "아니요"
			}
			w := cmd.OutOrStdout()
			fmt.Fprintf(w, "이름:      %s\nID:        %s\nowner:     %s\n외부 역할: %s\n범위 확장: %s\n\n권한 키 (%d개):\n",
				r.Name, r.Id, yes(r.IsOwner), yes(r.IsExternal), yes(r.ScopeExpandable), len(r.PermissionKeys))
			for _, k := range r.PermissionKeys {
				fmt.Fprintf(w, "  %s\n", k)
			}
			return nil
		},
	}
}

func newRolesCreateCmd(g *Env) *cobra.Command {
	var (
		perms            rolePermFlags
		external, expand bool
		dryRun           bool
	)
	cmd := &cobra.Command{
		Use:   "create <이름> (--all-permissions | --permission <키>...)",
		Short: "역할 만들기",
		Long: `새 역할을 만듭니다. 웹의 "조직 관리 > 역할 > 역할 추가"와 같습니다.
POST /api/authz/roles

--all-permissions는 내가 조직 전체에서 가진 권한 키를 모두 줍니다(--external이면 외부 역할에 줄 수 있는 키만).
역할을 만들거나 바꾸는 사람은 새로 주는 키를 모두 조직 전체에서 가져야 하기 때문입니다.
권한 키 목록과 설명은 '` + binName + ` api GET /api/authz/permissions'로 봅니다.
같은 이름의 역할이 이미 있으면 실패합니다. 확인 없이 바로 만들므로, 보낼 요청을 먼저 보려면 --dry-run을 붙이세요.
필요 권한: role.manage (조직 전체)`,
		Example: `  ` + binName + ` roles create 마케터 --permission report.read,report.export
  ` + binName + ` roles create 관리자 --all-permissions
  ` + binName + ` roles create 대행사 --external --scope-expandable -p report.read --dry-run`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !perms.set() {
				return errors.New("줄 권한을 정하세요: --permission <키>... 또는 --all-permissions")
			}
			if expand && !external {
				return errors.New("--scope-expandable은 --external과 함께 써야 합니다")
			}
			ctx := cmd.Context()
			c, err := g.apiClient()
			if err != nil {
				return err
			}
			keys, err := perms.resolve(ctx, c, external)
			if err != nil {
				return err
			}
			body := api.PostRoleInput{
				Name:            args[0],
				PermissionKeys:  toKeys[api.PostRoleInputPermissionKeys](keys),
				IsExternal:      Ptr(external),
				ScopeExpandable: Ptr(expand),
			}
			if dryRun {
				return g.printDryRun(cmd, writeRequest{
					summary: fmt.Sprintf("역할을 만듭니다: %s (권한 %d개)", args[0], len(keys)),
					method:  "POST", path: "/api/authz/roles", body: body,
				})
			}
			resp, err := c.CreateRoleWithResponse(ctx, body)
			if err != nil {
				return err
			}
			if err := CheckStatus(resp.HTTPResponse, resp.Body); err != nil {
				return err
			}
			if resp.JSON201 == nil {
				return fmt.Errorf("역할 응답을 JSON으로 읽지 못했습니다 (Content-Type: %s)", resp.HTTPResponse.Header.Get("Content-Type"))
			}
			if g.json {
				return g.out.JSONValue(resp.JSON201)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "역할을 만들었습니다: %s (ID %s, 권한 %d개)\n",
				resp.JSON201.Name, resp.JSON201.Id, len(resp.JSON201.PermissionKeys))
			return nil
		},
	}
	perms.bind(cmd)
	cmd.Flags().BoolVar(&external, "external", false, "외부 역할로 만듦 (게스트 초대용. 외부 역할에 줄 수 있는 권한만 가능)")
	cmd.Flags().BoolVar(&expand, "scope-expandable", false, "이 역할로 초대한 게스트에게 나중에 초대 범위 밖 권한도 줄 수 있음 (--external 필요)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "보낼 요청만 보여 주고 실행하지 않음")
	return cmd
}

func newRolesUpdateCmd(g *Env) *cobra.Command {
	var (
		perms  rolePermFlags
		name   string
		dryRun bool
	)
	cmd := &cobra.Command{
		Use:   "update <역할> [--name <새 이름>] [--all-permissions | --permission <키>...]",
		Short: "역할 이름이나 권한 키 바꾸기",
		Long: `역할의 이름이나 권한 키를 바꿉니다. 역할은 이름 전체나 ID로 지정합니다.
PATCH /api/authz/roles/{id}

--permission이나 --all-permissions를 주면 권한 키를 그 목록으로 통째로 바꿉니다(추가가 아니라 교체).
외부 역할 여부와 범위 확장은 바꿀 수 없습니다. owner 역할은 바꿀 수 없습니다.
확인 없이 바로 바꾸므로, 보낼 요청을 먼저 보려면 --dry-run을 붙이세요.
필요 권한: role.manage (조직 전체), 그리고 바꿀 역할보다 높은 역할`,
		Example: `  ` + binName + ` roles update 마케터 --name 마케팅 담당
  ` + binName + ` roles update 마케터 -p report.read,report.export,dashboard.manage --dry-run`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if name == "" && !perms.set() {
				return errors.New("바꿀 것을 정하세요: --name, --permission, --all-permissions 중 하나 이상")
			}
			ctx := cmd.Context()
			c, err := g.apiClient()
			if err != nil {
				return err
			}
			role, err := findRole(ctx, c, args[0])
			if err != nil {
				return err
			}
			var body api.PatchRoleInput
			var changes []string
			if name != "" {
				body.Name = &name
				changes = append(changes, fmt.Sprintf("이름 → %q", name))
			}
			if perms.set() {
				keys, err := perms.resolve(ctx, c, role.IsExternal)
				if err != nil {
					return err
				}
				body.PermissionKeys = Ptr(toKeys[api.PatchRoleInputPermissionKeys](keys))
				changes = append(changes, fmt.Sprintf("권한 %d개 → %d개", len(role.PermissionKeys), len(keys)))
			}
			path := "/api/authz/roles/" + role.Id
			if dryRun {
				return g.printDryRun(cmd, writeRequest{
					summary: fmt.Sprintf("역할을 바꿉니다: %s(%s) - %s", role.Name, role.Id, strings.Join(changes, ", ")),
					method:  "PATCH", path: path, body: body,
				})
			}
			resp, err := c.UpdateRoleWithResponse(ctx, role.Id, body)
			if err != nil {
				return err
			}
			if err := CheckStatus(resp.HTTPResponse, resp.Body); err != nil {
				return err
			}
			if resp.JSON200 == nil {
				return fmt.Errorf("역할 응답을 JSON으로 읽지 못했습니다 (Content-Type: %s)", resp.HTTPResponse.Header.Get("Content-Type"))
			}
			if g.json {
				return g.out.JSONValue(resp.JSON200)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "역할을 바꿨습니다: %s - %s\n", role.Name, strings.Join(changes, ", "))
			return nil
		},
	}
	perms.bind(cmd)
	cmd.Flags().StringVar(&name, "name", "", "새 이름")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "보낼 요청만 보여 주고 실행하지 않음")
	return cmd
}

func newRolesDeleteCmd(g *Env) *cobra.Command {
	var dryRun bool
	cmd := &cobra.Command{
		Use:     "delete <역할>",
		Aliases: []string{"rm"},
		Short:   "역할 지우기",
		Long: `역할을 지웁니다. 역할은 이름 전체나 ID로 지정합니다.
DELETE /api/authz/roles/{id}

아직 이 역할을 가진 멤버·팀이 있으면 실패합니다. owner 역할은 지울 수 없습니다.
확인 없이 바로 지우므로, 대상을 먼저 보려면 --dry-run을 붙이세요.
필요 권한: role.manage (조직 전체), 그리고 지울 역할보다 높은 역할`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			c, err := g.apiClient()
			if err != nil {
				return err
			}
			role, err := findRole(ctx, c, args[0])
			if err != nil {
				return err
			}
			if dryRun {
				return g.printDryRun(cmd, writeRequest{
					summary: fmt.Sprintf("역할을 지웁니다: %s(%s)", role.Name, role.Id),
					method:  "DELETE", path: "/api/authz/roles/" + role.Id,
				})
			}
			resp, err := c.DeleteRoleWithResponse(ctx, role.Id)
			if err != nil {
				return err
			}
			if err := CheckStatus(resp.HTTPResponse, resp.Body); err != nil {
				return err
			}
			if g.json {
				return g.out.JSONValue(map[string]any{"id": role.Id, "deleted": true})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "역할을 지웠습니다: %s(%s)\n", role.Name, role.Id)
			return nil
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "보낼 요청만 보여 주고 실행하지 않음")
	return cmd
}
