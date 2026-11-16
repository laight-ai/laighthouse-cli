// Package output은 명령 결과를 표 또는 JSON으로 출력한다.
package output

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
)

// Printer는 --json 플래그에 따라 출력 형식을 고른다.
type Printer struct {
	W    io.Writer
	JSON bool
}

// JSONValue는 v를 들여쓴 JSON으로 쓴다. --json이 아닐 때도 구조가 표로 안 맞는 결과에 쓴다.
func (p Printer) JSONValue(v any) error {
	enc := json.NewEncoder(p.W)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

// Table은 --json이면 rows의 원본(raw)을 JSON으로, 아니면 header/rows를 탭 정렬 표로 쓴다.
// raw는 보통 API 응답 그대로라 에이전트가 필드를 잃지 않고 받는다.
func (p Printer) Table(raw any, header []string, rows [][]string) error {
	if p.JSON {
		return p.JSONValue(raw)
	}
	tw := tabwriter.NewWriter(p.W, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, strings.Join(header, "\t"))
	for _, r := range rows {
		fmt.Fprintln(tw, strings.Join(r, "\t"))
	}
	return tw.Flush()
}
