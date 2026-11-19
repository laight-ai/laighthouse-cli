package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 상한은 웹·서버와 같은 50MB다. 그 아래 파일은 서버로 보내고, 넘으면 보내기 전에 막는다.
func TestUploadSizeLimitIs50MB(t *testing.T) {
	f := setup(t)
	sized := func(name string, n int64) string {
		p := filepath.Join(t.TempDir(), name)
		fh, err := os.Create(p)
		if err != nil {
			t.Fatal(err)
		}
		defer fh.Close()
		if err := fh.Truncate(n); err != nil {
			t.Fatal(err)
		}
		return p
	}

	// 예전 20MB 상한 바로 위 크기다. 가짜 서버가 내용이 달라 422로 답하는 것은 상관없다.
	_, err := run(t, f, "upload", sized("mid.csv", 20<<20+1), "--brand", "b1", "--date", "2026-10-01")
	if err != nil && strings.Contains(err.Error(), "MB를 넘습니다") {
		t.Fatalf("20MB를 조금 넘는 파일은 상한에 걸리지 않아야 하는데 %v", err)
	}
	if n := len(f.seen("POST", "/api/resources/b1/data-uploads")); n != 1 {
		t.Fatalf("20MB를 조금 넘는 파일은 서버로 보내야 하는데 %d번 보냄", n)
	}

	_, err = run(t, f, "upload", sized("big.csv", 50<<20+1), "--brand", "b1", "--date", "2026-10-01")
	if err == nil || !strings.Contains(err.Error(), "50MB") {
		t.Fatalf("50MB 초과 오류를 기대했는데 %v", err)
	}
	if n := len(f.seen("POST", "/api/resources/b1/data-uploads")); n != 1 {
		t.Fatalf("50MB를 넘는 파일은 보내지 않아야 하는데 요청이 %d번", n)
	}
}
