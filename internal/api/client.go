// Package api는 백엔드의 openapi.json에서 생성한 클라이언트다. gen.go는 손으로 고치지 않는다.
// 다시 만들려면 scripts/update-openapi.sh를 실행한다.
package api

import "net/http"

// Doer는 요청을 보내는 쪽이다. auth.Session이 이 인터페이스를 만족해 Bearer 토큰을 붙이고 갱신한다.
type Doer = HttpRequestDoer

// New는 baseURL(예: https://app.laight.ai)을 향하는 클라이언트를 만든다. 생성된 경로가 /api로 시작하므로
// baseURL에 /api를 붙이지 않는다.
func New(baseURL string, doer Doer) (*ClientWithResponses, error) {
	return NewClientWithResponses(baseURL, WithHTTPClient(doer))
}

var _ Doer = (*http.Client)(nil)
