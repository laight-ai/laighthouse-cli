package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/laight-ai/laighthouse-cli/internal/api"
	"github.com/laight-ai/laighthouse-cli/internal/config"
)

// apiClient는 저장된 토큰으로 백엔드 API를 부르는 생성 클라이언트를 만든다.
func (g *Env) apiClient() (*api.ClientWithResponses, error) {
	s, err := g.session()
	if err != nil {
		return nil, err
	}
	return api.New(g.baseURL, s)
}

// CheckStatus는 2xx가 아닌 응답을 오류로 바꾼다. 백엔드 오류 본문은 {"detail": ...} 모양이고
// 때로 reason이 붙는다. detail이 문자열이 아니면(422 검증 오류 등) JSON 그대로 보여 준다.
func CheckStatus(resp *http.Response, body []byte) error {
	if resp.StatusCode/100 == 2 {
		return nil
	}
	var parsed struct {
		Detail json.RawMessage `json:"detail"`
		Reason string          `json:"reason"`
	}
	msg := strings.TrimSpace(string(body))
	if json.Unmarshal(body, &parsed) == nil && len(parsed.Detail) > 0 {
		var s string
		if json.Unmarshal(parsed.Detail, &s) == nil {
			msg = s
		} else {
			msg = string(parsed.Detail)
		}
		if parsed.Reason != "" {
			msg += " (" + parsed.Reason + ")"
		}
	}
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		// 업로드처럼 서버가 토큰 교환을 거치는 경로는, 로그인 세션이 끝났으면 토큰이 아직 유효해도 거절한다.
		if parsed.Reason == "session_ended" || strings.HasPrefix(msg, "session_ended:") {
			return errors.New("로그인 세션이 끝났습니다. `" + binName + " login`으로 다시 로그인하세요")
		}
		return fmt.Errorf("인증 실패(401): %s. `"+binName+" login`으로 다시 로그인하세요", msg)
	case http.StatusForbidden:
		return fmt.Errorf("권한 없음(403): %s", msg)
	case http.StatusNotFound:
		return fmt.Errorf("찾을 수 없음(404): %s", msg)
	case http.StatusTooManyRequests:
		if after := resp.Header.Get("Retry-After"); after != "" {
			return fmt.Errorf("요청이 너무 잦습니다(429): %s. %s초 뒤에 다시 시도하세요", msg, after)
		}
		return fmt.Errorf("요청이 너무 잦습니다(429): %s", msg)
	}
	return fmt.Errorf("HTTP %d: %s", resp.StatusCode, msg)
}

// noArgs는 인자를 받지 않는 명령의 Args 검사다. 이런 명령에 붙은 인자는 대개 하위 명령을 잘못 친 것이라
// 비슷한 명령을 제안하고 실패한다. cobra는 그룹 명령에서 모르는 하위 명령을 받으면 도움말만 보여 주고
// 종료 코드 0으로 끝내는데, 에이전트가 이를 성공으로 오해한다.
func noArgs(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		return nil
	}
	return UnknownCommand(cmd, args[0])
}

func UnknownCommand(cmd *cobra.Command, arg string) error {
	if cmd.SuggestionsMinimumDistance <= 0 {
		cmd.SuggestionsMinimumDistance = 2
	}
	msg := fmt.Sprintf("%q는 %s의 하위 명령이 아닙니다", arg, cmd.CommandPath())
	if s := cmd.SuggestionsFor(arg); len(s) > 0 {
		msg += fmt.Sprintf(". 혹시 '%s %s'인가요?", cmd.CommandPath(), s[0])
	}
	return fmt.Errorf("%s ('%s --help'로 쓸 수 있는 명령 확인)", msg, cmd.CommandPath())
}

// NounCmd는 목록을 겸하는 명사 명령을 만든다. `laighthouse-cli members`만 쳐도 목록이 나오고, 습관대로
// `members list`(ls)를 쳐도 같은 동작을 한다. list 하위 명령은 도움말을 어지럽히지 않게 숨긴다.
func NounCmd(list func() *cobra.Command, use string, aliases []string, short, long string, subs ...*cobra.Command) *cobra.Command {
	cmd := list()
	cmd.Use, cmd.Aliases, cmd.Short, cmd.Long, cmd.Args = use, aliases, short, long, noArgs
	alias := list()
	alias.Use, alias.Aliases, alias.Hidden, alias.Args = "list", []string{"ls"}, true, noArgs
	cmd.AddCommand(alias)
	cmd.AddCommand(subs...)
	return cmd
}

// brandFlag는 브랜드가 필요한 명령에 --brand 플래그를 단다. 값은 Env.brand가 해석한다.
func brandFlag(cmd *cobra.Command, dst *string) {
	cmd.Flags().StringVarP(dst, "brand", "b", "",
		"브랜드 이름 또는 ID. 없으면 LAIGHTHOUSE_BRAND, 그다음 '"+binName+" brands use'로 정한 기본 브랜드")
}

// brand는 --brand 값을 브랜드 ID로 바꾼다. 이름과 ID 모두 받는다. 값이 없으면 LAIGHTHOUSE_BRAND,
// 그것도 없으면 `brands use`로 정한 기본 브랜드를 쓴다.
func (g *Env) brand(ctx context.Context, c *api.ClientWithResponses, flag string) (string, error) {
	q := flag
	if q == "" {
		q = os.Getenv("LAIGHTHOUSE_BRAND")
	}
	if q == "" {
		f, err := config.Load()
		if err != nil {
			return "", err
		}
		if b, ok := f.DefaultBrand[g.env]; ok {
			return b.ID, nil
		}
		return "", errors.New("브랜드를 정해야 합니다. '" + binName + " brands use <이름>'으로 기본 브랜드를 정하거나 --brand를 붙이세요")
	}
	b, err := findBrand(ctx, c, q)
	if err != nil {
		return "", err
	}
	return b.Uid, nil
}

func findBrand(ctx context.Context, c *api.ClientWithResponses, q string) (api.ListBrandOutput, error) {
	resp, err := c.ListBrandWithResponse(ctx)
	if err != nil {
		return api.ListBrandOutput{}, err
	}
	if err := CheckStatus(resp.HTTPResponse, resp.Body); err != nil {
		return api.ListBrandOutput{}, err
	}
	return Pick("브랜드", q, *resp.JSON200,
		func(b api.ListBrandOutput) string { return b.Uid },
		func(b api.ListBrandOutput) string { return b.Name })
}

// Pick은 사용자가 적은 값(ID 또는 이름)에 맞는 항목 하나를 고른다.
// ID 완전 일치, 이름 완전 일치(대소문자 무시), 이름 부분 일치 순서로 찾고, 그 단계에서 하나뿐일 때만 고른다.
// 이름은 유일하지 않을 수 있어 완전 일치가 여럿이어도 모호하다고 본다.
func Pick[T any](kind, query string, items []T, id, name func(T) string) (T, error) {
	var zero T
	q := strings.ToLower(query)
	for _, it := range items {
		if id(it) == query {
			return it, nil
		}
	}
	var exact, partial []T
	for _, it := range items {
		n := strings.ToLower(name(it))
		if n == q {
			exact = append(exact, it)
		}
		if strings.Contains(n, q) {
			partial = append(partial, it)
		}
	}
	if len(exact) > 0 {
		partial = exact
	}
	if len(partial) == 1 {
		return partial[0], nil
	}
	if len(items) == 0 {
		return zero, fmt.Errorf("%s 목록이 비어 있습니다", kind)
	}
	candidates := partial
	if len(candidates) == 0 {
		candidates = items
	}
	var names []string
	for i, it := range candidates {
		if i == 10 {
			names = append(names, "…")
			break
		}
		names = append(names, fmt.Sprintf("%s(%s)", name(it), id(it)))
	}
	if len(partial) > 1 {
		return zero, fmt.Errorf("%s 중 %q에 맞는 것이 여러 개입니다. ID로 지정하세요: %s", kind, query, strings.Join(names, ", "))
	}
	return zero, fmt.Errorf("%s 중 %q에 맞는 것이 없습니다. 있는 것: %s", kind, query, strings.Join(names, ", "))
}

// CursorFlags는 커서 방식(after/limit) 목록 명령의 공통 플래그다.
type CursorFlags struct {
	All   bool   // 커서가 끝날 때까지 모두 가져옴
	Limit int    // 한 번에 가져올 개수
	After string // 이어서 가져올 커서
}

func (f *CursorFlags) Bind(cmd *cobra.Command) {
	cmd.Flags().BoolVar(&f.All, "all", false, "끝까지 모두 가져옴")
	cmd.Flags().IntVar(&f.Limit, "limit", 50, "가져올 개수 (한 번에 최대 200)")
	cmd.Flags().StringVar(&f.After, "after", "", "이전 응답의 next_after 커서. 다음 페이지를 이어서 가져옴")
	// 커서는 에이전트가 직접 페이지를 넘길 때만 쓰므로 도움말에서는 숨긴다.
	_ = cmd.Flags().MarkHidden("after")
}

// Collect는 fetch를 커서가 끝날 때까지(또는 --all이 아니면 한 번만) 부른다.
// fetch는 (items, next_after, error)를 돌려준다.
func Collect[T any](f CursorFlags, fetch func(after *string, limit int) ([]T, *string, error)) ([]T, *string, error) {
	var after *string
	if f.After != "" {
		after = &f.After
	}
	var all []T
	for {
		items, next, err := fetch(after, f.Limit)
		if err != nil {
			return nil, nil, err
		}
		all = append(all, items...)
		if !f.All || next == nil || *next == "" {
			return all, next, nil
		}
		after = next
	}
}

// parseTTL은 7d, 12h, 90m 같은 기간을 읽는다. time.ParseDuration은 일(d) 단위를 모르므로 따로 처리한다.
func parseTTL(s string) (time.Duration, error) {
	if n, ok := strings.CutSuffix(s, "d"); ok {
		days, err := strconv.Atoi(n)
		if err != nil {
			return 0, fmt.Errorf("기간 형식이 잘못됐습니다: %q (예: 7d, 12h)", s)
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("기간 형식이 잘못됐습니다: %q (예: 7d, 12h)", s)
	}
	return d, nil
}

// openBrowser는 기본 브라우저로 url을 연다.
func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	return cmd.Start()
}

func Deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func Ptr[T any](v T) *T { return &v }
