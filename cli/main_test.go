package cli_test

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/laight-ai/laighthouse-cli/cli"
)

// 패키지 밖(ops 같은 다른 바이너리)에서 Options로 명령을 덧붙였을 때를 확인한다.

// probeCmd는 Env에서 받은 값을 Env.Out으로 쓴다. 요청 주소를 받으면 HTTPClient로 GET을 보낸다.
func probeCmd(env *cli.Env) *cobra.Command {
	return &cobra.Command{
		Use:   "probe [URL]",
		Short: "확장 명령 시험",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				c, err := env.HTTPClient()
				if err != nil {
					return err
				}
				req, _ := http.NewRequestWithContext(cmd.Context(), "GET", args[0], nil)
				resp, err := c.Do(req)
				if err != nil {
					return err
				}
				resp.Body.Close()
			}
			return env.Out().Table(map[string]any{"base_url": env.BaseURL()}, []string{"base_url", "json"},
				[][]string{{env.BaseURL(), fmt.Sprint(env.JSON())}})
		},
	}
}

func runMain(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	t.Setenv("LAIGHTHOUSE_CONFIG_DIR", t.TempDir()) // 로그인하지 않은 상태
	var out, errOut bytes.Buffer
	code = cli.Main(cli.Options{
		Name:     "lh-test",
		Version:  "v9",
		Commands: []func(*cli.Env) *cobra.Command{probeCmd},
		Stdout:   &out,
		Stderr:   &errOut,
	}, args)
	return code, out.String(), errOut.String()
}

// 덧붙인 명령이 전역 플래그가 반영된 Env를 받고, 결과를 Options.Stdout에 쓴다.
func TestMainRunsExtensionWithEnv(t *testing.T) {
	code, out, errOut := runMain(t, "--base-url", "http://h.test", "probe")
	if code != 0 || errOut != "" {
		t.Fatalf("성공해야 하는데 code=%d stderr=%q", code, errOut)
	}
	if !strings.Contains(out, "http://h.test") || !strings.Contains(out, "false") {
		t.Errorf("--base-url 값과 json=false가 표로 나와야 하는데:\n%s", out)
	}

	_, out, _ = runMain(t, "--json", "--base-url", "http://h.test", "probe")
	if strings.TrimSpace(out) != `{
  "base_url": "http://h.test"
}` {
		t.Errorf("--json이면 원본만 JSON으로 나와야 하는데:\n%s", out)
	}
}

// 도움말과 버전, 로그인 안내에는 Options.Name을 쓴다. 오류는 Options.Stderr로 가고 종료 코드는 1이다.
func TestMainUsesNameAndStderr(t *testing.T) {
	_, out, _ := runMain(t, "--help")
	if !strings.Contains(out, "lh-test login") || !strings.Contains(out, "probe") || strings.Contains(out, "laighthouse-cli") {
		t.Errorf("도움말에 lh-test 이름과 probe 명령이 나와야 하는데:\n%s", out)
	}
	if _, out, _ := runMain(t, "--version"); strings.TrimSpace(out) != "lh-test version v9" {
		t.Errorf("버전 출력이 다릅니다: %q", out)
	}

	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	code, out, errOut := runMain(t, "--base-url", srv.URL, "probe", srv.URL+"/api/x")
	if code != 1 || out != "" {
		t.Fatalf("로그인 전에는 실패해야 하는데 code=%d stdout=%q", code, out)
	}
	if !strings.Contains(errOut, "로그인이 필요합니다") || !strings.Contains(errOut, "`lh-test login`") {
		t.Errorf("Stderr에 lh-test 로그인 안내가 나와야 하는데: %q", errOut)
	}
}
