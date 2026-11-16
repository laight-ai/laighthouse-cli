package cli

import (
	"fmt"
	"strconv"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"
	"github.com/spf13/cobra"

	"github.com/laight-ai/laighthouse-cli/internal/api"
)

func newReportsCmd(g *Env) *cobra.Command {
	return NounCmd(func() *cobra.Command { return newReportsListCmd(g) },
		"reports", []string{"report"},
		"보고서 목록과 메타데이터",
		`브랜드의 보고서 목록을 봅니다. 웹의 "보고서" 목록과 같습니다.
GET /api/resources/{brand}/reports

보고서 본문(차트, 섹션)은 화면이 필요해 CLI에서 보여 주지 않습니다.
'`+binName+` reports show <ID> --web'으로 웹에서 여세요.

유형(--type): daily(일간), mtd(월누적), monthly(월간), dashboard(대시보드), calendar(캘린더)
--view:      mine(내가 만든 것) | published(공개된 것) | all
--published: all | published | unpublished
--from/--to: 보고서 기준 기간이 이 날짜 범위와 겹치는 것 (YYYY-MM-DD)
필요 권한: report.read (브랜드 범위)`,
		newReportsShowCmd(g))
}

func newReportsListCmd(g *Env) *cobra.Command {
	var (
		brand     string
		view      string
		types     string
		published string
		from, to  string
		author    string
		query     string
		limit     int
		all       bool
		page      int
		sortKey   string
		order     string
	)
	cmd := &cobra.Command{
		Use:   "list",
		Short: "보고서 목록",
		RunE: func(cmd *cobra.Command, _ []string) error {
			params := &api.GetBrandReportsParams{}
			if view != "" {
				params.View = Ptr(api.ReportView(view))
			}
			if types != "" {
				params.Types = &types
			}
			if published != "" {
				params.Published = Ptr(api.PublishedFilter(published))
			}
			var err error
			if params.PeriodStart, err = dateFlag(from, "--from"); err != nil {
				return err
			}
			if params.PeriodEnd, err = dateFlag(to, "--to"); err != nil {
				return err
			}
			if author != "" {
				params.Author = &author
			}
			if query != "" {
				params.Q = &query
			}
			if sortKey != "" {
				params.Sort = Ptr(api.BrandReportSortKey(sortKey))
			}
			if order != "" {
				params.Order = Ptr(api.SortOrder(order))
			}

			ctx := cmd.Context()
			c, err := g.apiClient()
			if err != nil {
				return err
			}
			brandID, err := g.brand(ctx, c, brand)
			if err != nil {
				return err
			}
			// 서버는 페이지 단위(page, page_size ≤ 100)라 --all이면 끝까지 넘긴다.
			size := limit
			if all {
				size = 100
			}
			params.PageSize = &size
			var out *api.GetBrandReportsOutput
			var reports []api.BrandReport
			for p := page; ; p++ {
				params.Page = Ptr(p)
				resp, err := c.GetBrandReportsWithResponse(ctx, brandID, params)
				if err != nil {
					return err
				}
				if err := CheckStatus(resp.HTTPResponse, resp.Body); err != nil {
					return err
				}
				out = resp.JSON200
				var got []api.BrandReport
				if out.Reports != nil {
					got = *out.Reports
				}
				reports = append(reports, got...)
				if !all || len(got) == 0 || len(reports) >= out.Total {
					break
				}
			}
			if all {
				out.Reports, out.Page, out.PageSize = &reports, 1, len(reports)
			}

			rows := make([][]string, 0, len(reports))
			for _, r := range reports {
				pub := ""
				if r.Published != nil {
					pub = "공개"
				}
				rows = append(rows, []string{string(r.Type), r.Name, r.ReferenceDate, r.CreatedBy, r.Created, pub, r.Uid})
			}
			if err := g.out.Table(out, []string{"유형", "이름", "기준일", "작성자", "생성", "상태", "ID"}, rows); err != nil {
				return err
			}
			if !g.json {
				fmt.Fprintf(cmd.OutOrStdout(), "\n%s: %d건 중 %d건", out.Name, out.Total, len(reports))
				if len(reports) < out.Total && !all {
					fmt.Fprint(cmd.OutOrStdout(), " (전부 보려면 --all)")
				}
				fmt.Fprintln(cmd.OutOrStdout())
			}
			return nil
		},
	}
	brandFlag(cmd, &brand)
	f := cmd.Flags()
	f.StringVar(&view, "view", "", "mine | published | all")
	f.StringVar(&types, "type", "", "유형 필터, 쉼표 구분 (daily,mtd,monthly,dashboard,calendar)")
	f.StringVar(&published, "published", "", "all | published | unpublished")
	f.StringVar(&from, "from", "", "기간 시작 (YYYY-MM-DD)")
	f.StringVar(&to, "to", "", "기간 끝 (YYYY-MM-DD)")
	f.StringVar(&author, "author", "", "작성자 표시명 (완전 일치)")
	f.StringVarP(&query, "query", "q", "", "보고서명 또는 작성자 부분 일치")
	f.IntVar(&limit, "limit", 50, "가져올 개수 (최대 100)")
	f.BoolVar(&all, "all", false, "끝까지 모두 가져옴")
	f.IntVar(&page, "page", 1, "시작 페이지 (1부터)")
	_ = f.MarkHidden("page")
	f.StringVar(&sortKey, "sort", "", "정렬 키")
	f.StringVar(&order, "order", "", "asc | desc")
	return cmd
}

func newReportsShowCmd(g *Env) *cobra.Command {
	var web bool
	cmd := &cobra.Command{
		Use:     "show <보고서 ID>",
		Aliases: []string{"get", "view", "open"},
		Short:   "보고서 메타데이터 (--web이면 브라우저로 열기)",
		Long: `보고서 하나의 메타데이터(이름, 유형, 기준일, 작성자, 공개 여부, 언어)를 봅니다.
본문(차트)은 --web으로 웹에서 여세요. 공개되지 않은 보고서는 작성자만 볼 수 있습니다.
GET /api/report/{id}`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			webURL := fmt.Sprintf("%s/m/report/%s", g.baseURL, args[0])
			if web {
				if err := openBrowser(webURL); err != nil {
					return fmt.Errorf("브라우저를 열지 못했습니다(%v). 직접 여세요: %s", err, webURL)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "브라우저에서 열었습니다: %s\n", webURL)
				return nil
			}
			c, err := g.apiClient()
			if err != nil {
				return err
			}
			resp, err := c.GetReportWithResponse(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if err := CheckStatus(resp.HTTPResponse, resp.Body); err != nil {
				return err
			}
			r := resp.JSON200
			if g.json {
				return g.out.JSONValue(r)
			}
			w := cmd.OutOrStdout()
			fmt.Fprintf(w, "ID:      %s\n이름:    %s\n유형:    %s\n브랜드:  %s\n기준일:  %s\n작성자:  %s\n생성:    %s\n언어:    %s\n",
				r.Uid, r.Name, r.Type, r.ResourceId, r.ReferenceDate, r.CreatedBy, r.Created.Local().Format(time.DateTime), r.Language)
			if r.Published != nil {
				fmt.Fprintf(w, "공개:    %s\n", r.Published.Local().Format(time.DateTime))
			} else {
				fmt.Fprintln(w, "공개:    아니오")
			}
			if r.IsRead != nil {
				fmt.Fprintf(w, "읽음:    %s\n", strconv.FormatBool(*r.IsRead))
			}
			fmt.Fprintf(w, "웹:      %s\n", webURL)
			return nil
		},
	}
	cmd.Flags().BoolVar(&web, "web", false, "브라우저에서 보고서 화면을 엶")
	return cmd
}

// dateFlag는 YYYY-MM-DD 문자열을 openapi Date로 바꾼다. 빈 값이면 nil.
func dateFlag(s, name string) (*openapi_types.Date, error) {
	if s == "" {
		return nil, nil
	}
	t, err := time.Parse(time.DateOnly, s)
	if err != nil {
		return nil, fmt.Errorf("%s는 YYYY-MM-DD 형식이어야 합니다: %q", name, s)
	}
	return &openapi_types.Date{Time: t}, nil
}
