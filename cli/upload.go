package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/laight-ai/laighthouse-cli/internal/api"
)

// 웹 업로드 화면, 서버(MAX_UPLOAD_SIZE_BYTES)와 같은 제한.
const uploadMaxBytes = 50 << 20

func newUploadCmd(g *Env) *cobra.Command {
	var (
		brand      string
		connection string
		reportDate string
		wait       bool
	)
	cmd := &cobra.Command{
		Use:   "upload <파일>",
		Short: "엑셀/CSV 데이터 업로드, 업로드 이력과 상태",
		Long: `매체에서 내려받은 엑셀(.xlsx)이나 CSV 파일(50MB 이하)을 브랜드에 올립니다. 웹의 "데이터 업로드"와 같습니다.
POST /api/resources/{brand}/data-uploads

파일은 브랜드의 "엑셀 연결"(어떤 매체의 파일인지)에 붙습니다. 연결이 하나뿐이면 자동으로 고르고,
여럿이면 --connection에 매체 이름 전체나 연결 ID를 적습니다('upload connections'로 확인).
--date는 파일에 날짜 열이 없는 매체에서 기준일을 정할 때 씁니다.

파일은 서버에서 비동기로 처리됩니다. --wait를 붙이면 끝날 때까지 기다리고,
아니면 'upload status <ID>'로 확인합니다. 지난 업로드는 'upload history'로 봅니다.
필요 권한: datasource.connect. 브랜드에 data_upload 기능이 켜져 있어야 합니다.`,
		Example: `  ` + binName + ` upload ./naver_2026-10.xlsx --wait
  ` + binName + ` upload ./sales.csv --connection naver --date 2026-10-01`,
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return errors.New("올릴 파일을 적으세요: " + binName + " upload <파일.xlsx|파일.csv>")
			}
			if len(args) > 1 {
				return errors.New("파일은 한 번에 하나씩 올립니다")
			}
			switch strings.ToLower(filepath.Ext(args[0])) {
			case ".xlsx", ".csv":
				return nil
			case "":
				return fmt.Errorf("%w. 파일이라면 .xlsx 또는 .csv여야 합니다", UnknownCommand(cmd, args[0]))
			}
			return fmt.Errorf(".xlsx 또는 .csv 파일만 올릴 수 있습니다: %s", args[0])
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			path := args[0]
			info, err := os.Stat(path)
			if err != nil {
				return err
			}
			if info.Size() > uploadMaxBytes {
				return fmt.Errorf("파일이 %dMB를 넘습니다 (%d bytes)", uploadMaxBytes>>20, info.Size())
			}
			if reportDate != "" {
				if _, err := time.Parse(time.DateOnly, reportDate); err != nil {
					return fmt.Errorf("--date는 YYYY-MM-DD 형식이어야 합니다: %q", reportDate)
				}
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
			conns, err := excelConnections(ctx, c, brandID)
			if err != nil {
				return err
			}
			conn, err := pickConnection(conns, connection)
			if err != nil {
				return err
			}

			body, contentType, err := multipartUpload(path, conn.Id, reportDate)
			if err != nil {
				return err
			}
			resp, err := c.UploadDataWithBodyWithResponse(ctx, brandID, contentType, body)
			if err != nil {
				return err
			}
			if err := CheckStatus(resp.HTTPResponse, resp.Body); err != nil {
				return err
			}
			acc := resp.JSON202
			if acc == nil {
				return g.out.JSONValue(rawJSON(resp.Body))
			}
			if !wait {
				if g.json {
					return g.out.JSONValue(acc)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "업로드 접수: %s → %s 연결 (upload_id=%d)\n상태 확인: "+binName+" upload status %d\n",
					filepath.Base(path), conn.Media, acc.UploadId, acc.UploadId)
				return nil
			}

			fmt.Fprintf(cmd.ErrOrStderr(), "업로드 접수(upload_id=%d). 처리 중", acc.UploadId)
			for {
				time.Sleep(2 * time.Second)
				st, err := c.GetDataUploadStatusWithResponse(ctx, brandID, acc.UploadId)
				if err != nil {
					return err
				}
				if err := CheckStatus(st.HTTPResponse, st.Body); err != nil {
					return err
				}
				d := st.JSON200
				if d.RunStatus == string(api.DataUploadItemStatusProcessing) {
					fmt.Fprint(cmd.ErrOrStderr(), ".")
					continue
				}
				fmt.Fprintln(cmd.ErrOrStderr())
				if g.json {
					return g.out.JSONValue(d)
				}
				printUploadDetail(cmd, acc.UploadId, d)
				if d.RunStatus == string(api.DataUploadItemStatusFailed) {
					return fmt.Errorf("업로드 처리 실패: %s", Deref(d.ErrorCode))
				}
				return nil
			}
		},
	}
	brandFlag(cmd, &brand)
	cmd.Flags().StringVarP(&connection, "connection", "c", "", "엑셀 연결의 매체 이름 전체 또는 ID (연결이 하나면 생략)")
	cmd.Flags().StringVar(&reportDate, "date", "", "기준일 (YYYY-MM-DD). 날짜 열이 없는 매체에서 필요")
	cmd.Flags().BoolVar(&wait, "wait", false, "처리가 끝날 때까지 기다림")
	cmd.AddCommand(newUploadHistoryCmd(g), newUploadStatusCmd(g), newUploadConnectionsCmd(g))
	return cmd
}

func excelConnections(ctx context.Context, c *api.ClientWithResponses, brandID string) ([]api.MediaConnectionResponse, error) {
	resp, err := c.ListMediaConnectionsWithResponse(ctx, brandID)
	if err != nil {
		return nil, err
	}
	if err := CheckStatus(resp.HTTPResponse, resp.Body); err != nil {
		return nil, err
	}
	var excel []api.MediaConnectionResponse
	for _, mc := range *resp.JSON200 {
		if mc.Kind == api.Excel {
			excel = append(excel, mc)
		}
	}
	return excel, nil
}

// pickConnection은 --connection 값(매체 이름 전체(대소문자 무시) 또는 ID)으로 엑셀 연결을 고른다.
// 다른 매체의 연결에 파일을 올리지 않게 Pick과 달리 이름 일부만 맞는 연결은 고르지 않는다.
// 값이 없고 연결이 하나뿐이면 그것을 쓴다.
func pickConnection(conns []api.MediaConnectionResponse, q string) (api.MediaConnectionResponse, error) {
	if q == "" && len(conns) == 1 {
		return conns[0], nil
	}
	if len(conns) == 0 {
		return api.MediaConnectionResponse{}, errors.New("이 브랜드에 엑셀 연결이 없습니다. 웹의 데이터 업로드 화면에서 매체를 먼저 연결하세요")
	}
	names := make([]string, 0, len(conns))
	for _, mc := range conns {
		names = append(names, fmt.Sprintf("%s(%d)", mc.Media, mc.Id))
	}
	if q == "" {
		return api.MediaConnectionResponse{}, fmt.Errorf("엑셀 연결이 여러 개입니다. --connection으로 고르세요: %s", strings.Join(names, ", "))
	}
	var exact []api.MediaConnectionResponse
	for _, mc := range conns {
		if strconv.Itoa(mc.Id) == q || strings.EqualFold(mc.Media, q) {
			exact = append(exact, mc)
		}
	}
	if len(exact) == 0 {
		return api.MediaConnectionResponse{}, fmt.Errorf("매체 이름이나 ID가 %q인 엑셀 연결이 없습니다. 매체 이름 전체를 적으세요. 있는 연결: %s", q, strings.Join(names, ", "))
	}
	if len(exact) > 1 {
		dup := make([]string, 0, len(exact))
		for _, mc := range exact {
			dup = append(dup, fmt.Sprintf("%s(%d)", mc.Media, mc.Id))
		}
		return api.MediaConnectionResponse{}, fmt.Errorf("매체가 %q인 엑셀 연결이 여러 개입니다. ID로 지정하세요: %s", q, strings.Join(dup, ", "))
	}
	return exact[0], nil
}

// multipartUpload는 웹과 같은 필드(connection_id, report_date, file)로 multipart 본문을 만든다.
func multipartUpload(path string, connection int, reportDate string) (io.Reader, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, "", err
	}
	defer f.Close()

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if err := mw.WriteField("connection_id", strconv.Itoa(connection)); err != nil {
		return nil, "", err
	}
	if reportDate != "" {
		if err := mw.WriteField("report_date", reportDate); err != nil {
			return nil, "", err
		}
	}
	part, err := mw.CreateFormFile("file", filepath.Base(path))
	if err != nil {
		return nil, "", err
	}
	if _, err := io.Copy(part, f); err != nil {
		return nil, "", err
	}
	if err := mw.Close(); err != nil {
		return nil, "", err
	}
	return &buf, mw.FormDataContentType(), nil
}

func newUploadConnectionsCmd(g *Env) *cobra.Command {
	var brand string
	cmd := &cobra.Command{
		Use:   "connections",
		Short: "브랜드의 엑셀 연결 목록 (GET /api/resources/{brand}/media-connections)",
		Long: `업로드 대상이 되는 엑셀 연결을 봅니다. "기준일: 필요"인 연결은 업로드할 때 --date가 필요합니다.
연결이 없으면 웹의 데이터 업로드 화면에서 매체를 먼저 연결하세요.`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			c, err := g.apiClient()
			if err != nil {
				return err
			}
			brandID, err := g.brand(ctx, c, brand)
			if err != nil {
				return err
			}
			excel, err := excelConnections(ctx, c, brandID)
			if err != nil {
				return err
			}
			rows := make([][]string, 0, len(excel))
			for _, mc := range excel {
				dateReq := ""
				if mc.ReportDateRequired != nil && *mc.ReportDateRequired {
					dateReq = "필요"
				}
				rows = append(rows, []string{mc.Media, Deref(mc.DisplayName), mc.Status, dateReq, strconv.Itoa(mc.Id)})
			}
			return g.out.Table(excel, []string{"매체", "이름", "상태", "기준일", "ID"}, rows)
		},
	}
	brandFlag(cmd, &brand)
	return cmd
}

func newUploadHistoryCmd(g *Env) *cobra.Command {
	var (
		brand  string
		limit  int
		all    bool
		offset int
		status string
	)
	cmd := &cobra.Command{
		Use:   "history",
		Short: "지난 업로드 이력 (GET /api/resources/{brand}/data-uploads)",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			c, err := g.apiClient()
			if err != nil {
				return err
			}
			brandID, err := g.brand(ctx, c, brand)
			if err != nil {
				return err
			}
			params := &api.GetDataUploadHistoryParams{Limit: &limit}
			if status != "" {
				params.Status = Ptr(api.GetDataUploadHistoryParamsStatus(status))
			}
			var out *api.DataUploadListResponse
			var items []api.DataUploadItem
			for off := offset; ; off += limit {
				params.Offset = Ptr(off)
				resp, err := c.GetDataUploadHistoryWithResponse(ctx, brandID, params)
				if err != nil {
					return err
				}
				if err := CheckStatus(resp.HTTPResponse, resp.Body); err != nil {
					return err
				}
				out = resp.JSON200
				items = append(items, out.Items...)
				if !all || len(out.Items) == 0 || off+len(out.Items) >= out.Total {
					break
				}
			}
			if all {
				out.Items, out.Offset, out.Limit = items, offset, len(items)
			}
			rows := make([][]string, 0, len(items))
			for _, u := range items {
				rowCount := ""
				if u.RowCount != nil {
					rowCount = strconv.Itoa(*u.RowCount)
				}
				rows = append(rows, []string{u.CreatedAt.Local().Format(time.DateTime), u.Source, Deref(u.FileName), string(u.Status),
					Deref(u.ReportDate), rowCount, Deref(u.UploaderEmail), Deref(u.ErrorCode), strconv.Itoa(u.UploadId)})
			}
			if err := g.out.Table(out, []string{"시각", "매체", "파일", "상태", "기준일", "행 수", "업로더", "오류", "ID"}, rows); err != nil {
				return err
			}
			if !g.json {
				fmt.Fprintf(cmd.OutOrStdout(), "\n%d건 중 %d건", out.Total, len(items))
				if offset+len(items) < out.Total && !all {
					fmt.Fprint(cmd.OutOrStdout(), " (전부 보려면 --all)")
				}
				fmt.Fprintln(cmd.OutOrStdout())
			}
			return nil
		},
	}
	brandFlag(cmd, &brand)
	cmd.Flags().IntVar(&limit, "limit", 50, "가져올 개수")
	cmd.Flags().BoolVar(&all, "all", false, "끝까지 모두 가져옴")
	cmd.Flags().IntVar(&offset, "offset", 0, "건너뛸 개수")
	_ = cmd.Flags().MarkHidden("offset")
	cmd.Flags().StringVar(&status, "status", "", "processing | success | failed | mapping_needed")
	return cmd
}

func newUploadStatusCmd(g *Env) *cobra.Command {
	var brand string
	cmd := &cobra.Command{
		Use:   "status <업로드 ID>",
		Short: "업로드 처리 상태 (GET /api/resources/{brand}/data-uploads/{id})",
		Long: `업로드 한 건의 처리 상태를 봅니다. success, failed, processing, mapping_needed 중 하나이며,
mapping_needed는 열 이름 매핑이 필요한 상태로 웹에서 이어서 처리합니다.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.Atoi(args[0])
			if err != nil {
				return fmt.Errorf("업로드 ID는 숫자여야 합니다: %q", args[0])
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
			resp, err := c.GetDataUploadStatusWithResponse(ctx, brandID, id)
			if err != nil {
				return err
			}
			if err := CheckStatus(resp.HTTPResponse, resp.Body); err != nil {
				return err
			}
			if g.json {
				return g.out.JSONValue(resp.JSON200)
			}
			printUploadDetail(cmd, id, resp.JSON200)
			return nil
		},
	}
	brandFlag(cmd, &brand)
	return cmd
}

func printUploadDetail(cmd *cobra.Command, id int, d *api.DataUploadDetail) {
	w := cmd.OutOrStdout()
	fmt.Fprintf(w, "upload_id: %d\n상태:      %s\n매체:      %s\n", id, d.RunStatus, d.Source)
	if d.ReportDate != nil {
		fmt.Fprintf(w, "기준일:    %s\n", *d.ReportDate)
	}
	if d.DateFrom != nil || d.DateTo != nil {
		fmt.Fprintf(w, "데이터 기간: %s ~ %s\n", Deref(d.DateFrom), Deref(d.DateTo))
	}
	if d.RowCount != nil {
		fmt.Fprintf(w, "행 수:     %d\n", *d.RowCount)
	}
	if d.MappingNeeded {
		fmt.Fprintln(w, "열 매핑이 필요합니다. 웹의 데이터 업로드 화면에서 이어서 처리하세요.")
	}
	if d.ErrorCode != nil {
		fmt.Fprintf(w, "오류:      %s\n", *d.ErrorCode)
	}
}
