package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

// rawJSON은 응답 본문을 다시 들여쓰기해 출력하기 위한 래퍼다. JSON이 아니면 문자열로 낸다.
type rawJSON []byte

func (r rawJSON) MarshalJSON() ([]byte, error) {
	if json.Valid(r) {
		return r, nil
	}
	return json.Marshal(string(r))
}

func newAPICmd(g *Env) *cobra.Command {
	var (
		data    string
		headers []string
	)
	cmd := &cobra.Command{
		Use:   "api <METHOD> <path>",
		Short: "임의의 API 엔드포인트를 로그인 토큰으로 호출",
		Long: `전용 명령이 없는 엔드포인트를 직접 부릅니다. 로그인 토큰을 붙이고 만료되면 갱신합니다.
응답 본문은 JSON이면 들여써서, 아니면 그대로 출력합니다. 2xx가 아니면 종료 코드 1입니다.

경로는 /api/... 또는 /auth/... 로 시작합니다. 전체 목록은 {base-url}/api/docs 에 있습니다.

예:
  ` + binName + ` api GET /api/authz/me/permissions
  ` + binName + ` api GET "/api/authz/audit?limit=20"
  ` + binName + ` api POST /api/authz/teams --data '{"name":"그로스팀"}'
  ` + binName + ` api POST /api/authz/invites/accept --data @body.json
  ` + binName + ` api PATCH /api/report/<id>/published --data '{"published":true}'`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			method := strings.ToUpper(args[0])
			path := args[1]
			if !strings.HasPrefix(path, "/") {
				return fmt.Errorf("경로는 /로 시작해야 합니다: %q", path)
			}

			var body io.Reader
			if data != "" {
				b := []byte(data)
				if strings.HasPrefix(data, "@") {
					var err error
					if data == "@-" {
						b, err = io.ReadAll(cmd.InOrStdin())
					} else {
						b, err = os.ReadFile(data[1:])
					}
					if err != nil {
						return err
					}
				}
				body = bytes.NewReader(b)
			}
			req, err := http.NewRequestWithContext(cmd.Context(), method, g.baseURL+path, body)
			if err != nil {
				return err
			}
			if body != nil {
				req.Header.Set("Content-Type", "application/json")
			}
			for _, h := range headers {
				k, v, ok := strings.Cut(h, ":")
				if !ok {
					return fmt.Errorf("--header는 '이름: 값' 형식이어야 합니다: %q", h)
				}
				req.Header.Set(strings.TrimSpace(k), strings.TrimSpace(v))
			}

			s, err := g.session()
			if err != nil {
				return err
			}
			resp, err := s.Do(req)
			if err != nil {
				return err
			}
			defer resp.Body.Close()
			respBody, err := io.ReadAll(resp.Body)
			if err != nil {
				return err
			}
			if len(respBody) > 0 {
				if json.Valid(respBody) {
					if err := g.out.JSONValue(rawJSON(respBody)); err != nil {
						return err
					}
				} else {
					fmt.Fprintln(cmd.OutOrStdout(), string(respBody))
				}
			}
			if resp.StatusCode/100 != 2 {
				return fmt.Errorf("HTTP %d", resp.StatusCode)
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&data, "data", "d", "", "요청 본문(JSON). @파일 또는 @-(표준 입력)도 가능")
	cmd.Flags().StringArrayVarP(&headers, "header", "H", nil, "추가 헤더 '이름: 값' (예: 'X-Acting-Tenant: <id>')")
	return cmd
}
