// Package cli는 laighthouse 명령 트리를 정의한다.
//
// 명령 이름은 사용자가 도움말 없이 맞힐 수 있게 짓는다.
//   - 자주 쓰는 동작은 최상위 동사: login, logout, whoami, upload, invite
//   - 동작에 딸린 이력·관리는 그 동사 아래: upload history, invite list
//   - 나머지는 명사이고 명사만 쳐도 목록: brands, members, teams, roles, reports
//
// 다른 바이너리가 이 명령 트리에 명령을 덧붙이려면 Options.Commands를 쓴다. 내보낸 함수와 타입은
// 그런 확장 명령을 위한 것이다. v0 동안에는 하위 호환을 보장하지 않는다.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/spf13/cobra"

	"github.com/laight-ai/laighthouse-cli/internal/auth"
	"github.com/laight-ai/laighthouse-cli/internal/config"
	"github.com/laight-ai/laighthouse-cli/output"
)

// binName은 도움말과 오류 메시지에 적는 명령 이름이다. Main이 Options.Name으로 바꾼다.
var binName = "laighthouse-cli"

// Options는 Main이 만들 바이너리를 정한다.
type Options struct {
	// Name은 명령 이름이다. 비우면 laighthouse-cli.
	Name    string
	Version string
	// Commands는 루트에 덧붙일 명령을 만든다. 각 함수는 공용 상태 Env를 받는다.
	Commands []func(*Env) *cobra.Command
	// Stdout과 Stderr는 결과와 오류를 쓸 곳이다. 비우면 os.Stdout, os.Stderr. 테스트에서 바꾼다.
	Stdout io.Writer
	Stderr io.Writer
}

// Env는 모든 명령이 공유하는 상태다. 전역 플래그 값은 명령을 실행할 때 채워진다.
type Env struct {
	envName string
	baseURL string
	json    bool

	env config.Env
	out output.Printer
}

// BaseURL은 요청을 보낼 서비스 주소다.
func (g *Env) BaseURL() string { return g.baseURL }

// JSON은 --json을 붙였는지 알려 준다.
func (g *Env) JSON() bool { return g.json }

// Out은 --json에 맞춰 결과를 쓰는 출력기다.
func (g *Env) Out() output.Printer { return g.out }

// HTTPClient는 저장된 토큰을 붙이고, 만료되면 갱신해서 요청을 보내는 클라이언트다.
func (g *Env) HTTPClient() (interface {
	Do(*http.Request) (*http.Response, error)
}, error) {
	return g.session()
}

// Main은 명령을 실행하고 종료 코드를 돌려준다.
func Main(opts Options, args []string) int {
	if opts.Name != "" {
		prev := binName
		binName = opts.Name
		defer func() { binName = prev }()
	}
	stdout, stderr := opts.Stdout, opts.Stderr
	if stdout == nil {
		stdout = os.Stdout
	}
	if stderr == nil {
		stderr = os.Stderr
	}
	root, _ := newApp(opts)
	root.SetOut(stdout)
	root.SetErr(stderr)
	root.SetArgs(args)
	if err := root.ExecuteContext(context.Background()); err != nil {
		if errors.Is(err, auth.ErrNotLoggedIn) {
			err = fmt.Errorf("%w. `%s login`을 먼저 실행하세요", err, binName)
		}
		fmt.Fprintln(stderr, "오류:", err)
		return 1
	}
	return 0
}

func newRoot(version string) *cobra.Command {
	root, _ := newApp(Options{Version: version})
	return root
}

// resolveEnv는 --env와 --base-url을 읽어 접속 환경과 주소를 정한다.
func (g *Env) resolveEnv() error {
	env, err := config.ParseEnv(g.envName)
	if err != nil {
		return err
	}
	g.env = env
	if g.baseURL == "" {
		g.baseURL = env.BaseURL()
	}
	return nil
}

func newApp(opts Options) (*cobra.Command, *Env) {
	g := &Env{}

	root := &cobra.Command{
		Use:     binName,
		Short:   "laighthouse 서비스를 터미널에서 다루는 CLI",
		Version: opts.Version,
		Long: `laighthouse 서비스를 브라우저 대신 터미널에서 다룹니다.

멤버·조직 조회, 초대, 보고서 목록, 엑셀 업로드처럼 화면이 없어도 되는 기능을
제공합니다. 차트처럼 화면이 필요한 기능은 웹(https://app.laight.ai)을 쓰세요.

시작하기:
  ` + binName + ` login
  ` + binName + ` brands use <브랜드 이름>
  ` + binName + ` reports

에이전트가 쓸 때는 --json을 붙이면 모든 명령이 API 응답을 그대로 JSON으로 냅니다.
각 명령의 --help에 그 기능이 무엇인지, 어떤 권한이 필요한지 적혀 있습니다.`,
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			if err := g.resolveEnv(); err != nil {
				return err
			}
			g.out = output.Printer{W: cmd.OutOrStdout(), JSON: g.json}
			return nil
		},
	}

	defaultEnv := os.Getenv("LAIGHTHOUSE_ENV")
	if defaultEnv == "" {
		defaultEnv = string(config.EnvProd)
	}
	pf := root.PersistentFlags()
	pf.StringVar(&g.envName, "env", defaultEnv, "접속 환경 (dev|prod). 환경변수 LAIGHTHOUSE_ENV로도 지정")
	pf.StringVar(&g.baseURL, "base-url", "", "서비스 주소를 직접 지정 (로컬 개발용, 기본값은 --env에 따름)")
	pf.BoolVar(&g.json, "json", false, "결과를 표 대신 JSON으로 출력")

	root.AddCommand(
		newLoginCmd(g),
		newLogoutCmd(g),
		newWhoamiCmd(g),
		newBrandsCmd(g),
		newMembersCmd(g),
		newInviteCmd(g),
		newTeamsCmd(g),
		newRolesCmd(g),
		newReportsCmd(g),
		newUploadCmd(g),
		newAPICmd(g),
	)
	for _, c := range opts.Commands {
		root.AddCommand(c(g))
	}
	return root, g
}
