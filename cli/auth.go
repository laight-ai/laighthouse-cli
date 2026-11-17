package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/laight-ai/laighthouse-cli/internal/api"
	"github.com/laight-ai/laighthouse-cli/internal/auth"
	"github.com/laight-ai/laighthouse-cli/internal/config"
)

func (g *Env) authClient() *auth.Client {
	return &auth.Client{BaseURL: g.baseURL}
}

// session은 저장된 토큰으로 API를 부를 수 있는 세션을 만든다. 로그인이 안 돼 있어도 오류를 내지 않고,
// 첫 요청 때 ErrNotLoggedIn이 난다.
func (g *Env) session() (*auth.Session, error) {
	store, err := auth.DefaultStore()
	if err != nil {
		return nil, err
	}
	s := &auth.Session{Env: g.env, Store: store, Client: g.authClient()}
	if g.header != nil {
		s.Header = g.header()
	}
	return s, nil
}

func newLoginCmd(g *Env) *cobra.Command {
	var username string
	var passwordStdin, withPassword, noBrowser bool
	cmd := &cobra.Command{
		Use:   "login",
		Short: "브라우저로 로그인 (비밀번호는 --password)",
		Long: `로그인해 토큰을 저장합니다. 기본은 브라우저 로그인입니다.

브라우저 로그인: 브라우저가 열리면 터미널에 나온 기기 코드를 입력하고, 웹에서처럼
로그인(비밀번호, Google, Microsoft)한 뒤 승인합니다. 브라우저가 없는 곳(SSH 등)에서는
--no-browser로 주소와 코드만 출력하고, 다른 기기에서 그 주소를 열어 코드를 입력하면 됩니다.

비밀번호 로그인: --password를 주면 이메일과 비밀번호를 받습니다. Google/Microsoft로만 가입한
계정은 비밀번호가 없어 브라우저 로그인을 써야 합니다. 스크립트나 에이전트에서는:
  echo "$LAIGHTHOUSE_PASSWORD" | ` + binName + ` login --username me@laight.ai --password-stdin
(--username이나 --password-stdin을 주면 --password로 봅니다.)

토큰은 환경(dev/prod)별로 ~/.config/laighthouse/credentials.json에 저장되며,
다른 명령이 자동으로 꺼내 쓰고 만료되면 갱신합니다.`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !withPassword && username == "" && !passwordStdin {
				tokens, err := browserLogin(cmd, g, noBrowser)
				if err != nil {
					return err
				}
				return saveLogin(cmd, g, tokens)
			}
			stdin := cmd.InOrStdin()
			if username == "" {
				fmt.Fprint(cmd.ErrOrStderr(), "이메일: ")
				line, err := bufio.NewReader(stdin).ReadString('\n')
				if err != nil && !errors.Is(err, io.EOF) {
					return err
				}
				username = strings.TrimSpace(line)
			}
			if username == "" {
				return errors.New("--username이 필요합니다")
			}
			password, err := readPassword(cmd, passwordStdin)
			if err != nil {
				return err
			}

			tokens, err := g.authClient().Login(cmd.Context(), username, password)
			if err != nil {
				return fmt.Errorf("로그인 실패: %w", err)
			}
			return saveLogin(cmd, g, tokens)
		},
	}
	cmd.Flags().BoolVar(&withPassword, "password", false, "브라우저 대신 이메일과 비밀번호로 로그인")
	cmd.Flags().BoolVar(&noBrowser, "no-browser", false, "브라우저를 열지 않고 로그인 주소와 코드만 출력")
	cmd.Flags().StringVarP(&username, "username", "u", "", "비밀번호 로그인 이메일 (없으면 물어봄)")
	cmd.Flags().BoolVar(&passwordStdin, "password-stdin", false, "비밀번호를 표준 입력에서 읽음")
	return cmd
}

// browserLogin은 디바이스 코드 플로우로 로그인한다. 안내는 stderr로 내보내 --json 출력과 섞이지 않게 한다.
func browserLogin(cmd *cobra.Command, g *Env, noBrowser bool) (auth.Tokens, error) {
	ctx := cmd.Context()
	c := g.authClient()
	d, err := c.StartDeviceLogin(ctx)
	if errors.Is(err, auth.ErrDeviceLoginUnavailable) {
		return auth.Tokens{}, fmt.Errorf("%s 환경은 아직 브라우저 로그인을 지원하지 않습니다. '"+binName+" login --password'로 로그인하세요", g.env)
	}
	if err != nil {
		return auth.Tokens{}, fmt.Errorf("로그인 실패: %w", err)
	}
	w := cmd.ErrOrStderr()
	fmt.Fprintf(w, "브라우저에서 기기 코드를 입력하고, 로그인한 뒤 승인하세요.\n\n  기기 코드: %s\n  주소:      %s\n\n", d.UserCode, d.VerificationURI)
	if !noBrowser && openBrowser(d.VerificationURI) != nil {
		fmt.Fprintln(w, "브라우저를 열지 못했습니다. 위 주소를 직접 여세요.")
	}
	fmt.Fprintln(w, "승인을 기다리는 중…")
	tokens, err := c.WaitDeviceLogin(ctx, d)
	if err != nil {
		return auth.Tokens{}, fmt.Errorf("로그인 실패: %w", err)
	}
	return tokens, nil
}

func saveLogin(cmd *cobra.Command, g *Env, tokens auth.Tokens) error {
	store, err := auth.DefaultStore()
	if err != nil {
		return err
	}
	if err := store.Save(g.env, tokens); err != nil {
		return err
	}
	user, _ := auth.UserFromToken(tokens.AccessToken)
	if g.json {
		return g.out.JSONValue(map[string]any{"env": g.env, "username": user.Email, "logged_in": true})
	}
	fmt.Fprintf(cmd.OutOrStdout(), "%s 환경에 %s 계정으로 로그인했습니다.\n", g.env, user.Email)
	return nil
}

func readPassword(cmd *cobra.Command, fromStdin bool) (string, error) {
	if fromStdin {
		b, err := io.ReadAll(cmd.InOrStdin())
		if err != nil {
			return "", err
		}
		return strings.TrimRight(string(b), "\r\n"), nil
	}
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return "", errors.New("터미널이 아니면 --password-stdin으로 비밀번호를 넘기세요")
	}
	fmt.Fprint(cmd.ErrOrStderr(), "비밀번호: ")
	b, err := term.ReadPassword(fd)
	fmt.Fprintln(cmd.ErrOrStderr())
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func newLogoutCmd(g *Env) *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "서버 세션을 끝내고 저장된 토큰 삭제",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := g.session()
			if err != nil {
				return err
			}
			tokens, err := s.Tokens()
			if errors.Is(err, auth.ErrNotLoggedIn) {
				fmt.Fprintf(cmd.OutOrStdout(), "%s 환경에 저장된 로그인이 없습니다.\n", g.env)
				return nil
			}
			if err != nil {
				return err
			}
			// 서버 로그아웃이 실패해도 로컬 토큰은 지운다.
			serverErr := g.authClient().Logout(cmd.Context(), tokens)
			if err := s.Store.Delete(g.env); err != nil {
				return err
			}
			if serverErr != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "서버 세션 종료 실패(로컬 토큰은 삭제함): %v\n", serverErr)
			}
			if g.json {
				return g.out.JSONValue(map[string]any{"env": g.env, "logged_in": false})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s 환경에서 로그아웃했습니다.\n", g.env)
			return nil
		},
	}
}

// tenantMembership은 GET /api/authz/me 응답이다.
type tenantMembership struct {
	TenantID   string `json:"tenant_id"`
	Name       string `json:"name"`
	Kind       string `json:"kind"`
	TenantKind string `json:"tenant_kind"`
	Acting     bool   `json:"acting"`
}

func newWhoamiCmd(g *Env) *cobra.Command {
	var withPermissions bool
	cmd := &cobra.Command{
		Use:   "whoami",
		Short: "로그인한 계정, 소속 조직, 기본 브랜드 확인",
		Long: `로그인한 계정(이메일, 이름, 역할)과 소속 조직(테넌트), 'brands use'로 정한 기본 브랜드를 보여 줍니다.
토큰이 만료됐으면 갱신을 시도하므로 로그인 상태 점검에도 쓸 수 있습니다.

--permissions를 주면 내가 가진 권한 키를 범위(조직 전체 또는 브랜드)별로 함께 보여 줍니다.
GET /api/authz/me/permissions`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := g.session()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			token, err := s.AccessToken(ctx)
			if err != nil {
				return err
			}
			user, err := auth.UserFromToken(token)
			if err != nil {
				return err
			}
			tenant, err := fetchTenant(ctx, s, g.baseURL)
			if err != nil {
				return err
			}
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			var brand *config.Brand
			if b, ok := cfg.DefaultBrand[g.env]; ok {
				brand = &b
			}
			var perms *api.HeldPermissionsOutput
			// 소속 조직이 없으면 권한 조회도 403이므로 묻지 않는다.
			if withPermissions && tenant != nil {
				c, err := api.New(g.baseURL, s)
				if err != nil {
					return err
				}
				resp, err := c.GetMyHeldPermissionsWithResponse(ctx)
				if err != nil {
					return err
				}
				if err := CheckStatus(resp.HTTPResponse, resp.Body); err != nil {
					return err
				}
				if resp.JSON200 == nil {
					return fmt.Errorf("권한 응답을 JSON으로 읽지 못했습니다 (Content-Type: %s)", resp.HTTPResponse.Header.Get("Content-Type"))
				}
				perms = resp.JSON200
			}

			if g.json {
				v := map[string]any{"env": g.env, "user": user, "tenant": tenant, "default_brand": brand}
				if withPermissions {
					v["permissions"] = perms
				}
				return g.out.JSONValue(v)
			}
			w := cmd.OutOrStdout()
			fmt.Fprintf(w, "환경:   %s\n", g.env)
			fmt.Fprintf(w, "계정:   %s (%s)\n", user.Email, user.Name)
			fmt.Fprintf(w, "UID:    %s\n", user.UID)
			if len(user.Roles) > 0 {
				fmt.Fprintf(w, "역할:   %s\n", strings.Join(user.Roles, ", "))
			}
			if tenant == nil {
				fmt.Fprintln(w, "조직:   없음 (초대를 받아야 합니다)")
			} else {
				fmt.Fprintf(w, "조직:   %s [%s] (%s)\n", tenant.Name, tenant.TenantID, tenant.Kind)
			}
			if brand == nil {
				fmt.Fprintln(w, "브랜드: 기본 브랜드 없음 ('"+binName+" brands use <이름>'으로 지정)")
			} else {
				fmt.Fprintf(w, "브랜드: %s [%s]\n", brand.Name, brand.ID)
			}
			if withPermissions && perms == nil {
				fmt.Fprintln(w, "권한:   없음")
			} else if perms != nil {
				fmt.Fprintln(w, "\n권한:")
				return g.out.Table(nil, []string{"범위", "권한 키"}, heldScopeRows(perms.Items))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&withPermissions, "permissions", false, "내가 가진 권한 키를 범위별로 함께 표시")
	return cmd
}

// fetchTenant는 소속 조직을 조회한다. 403은 "소속 조직 없음"이므로 nil을 돌려준다.
func fetchTenant(ctx context.Context, s *auth.Session, baseURL string) (*tenantMembership, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/api/authz/me", nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		var t tenantMembership
		if err := json.NewDecoder(resp.Body).Decode(&t); err != nil {
			return nil, err
		}
		return &t, nil
	case http.StatusForbidden:
		return nil, nil
	default:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return nil, fmt.Errorf("조직 조회 실패: HTTP %d %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
}
