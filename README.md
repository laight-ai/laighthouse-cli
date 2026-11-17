# laighthouse-cli

laighthouse 서비스를 브라우저 대신 터미널에서 다루는 CLI입니다. 멤버·조직 조회, 초대, 보고서 목록, 엑셀 업로드처럼 화면이 없어도 되는 기능을 제공합니다. 사람이 직접 치는 용도와 에이전트가 셸로 부르는 용도를 겸합니다.

## 설치

```sh
go install github.com/laight-ai/laighthouse-cli/cmd/laighthouse-cli@latest
```

GitHub Releases에서 OS별 바이너리를 직접 받을 수도 있습니다.

## 시작

```sh
laighthouse-cli login                      # 브라우저 로그인 (prod)
laighthouse-cli login --password           # 이메일·비밀번호
laighthouse-cli whoami
laighthouse-cli whoami --permissions       # 내가 가진 권한 키 (범위별)
laighthouse-cli brands                     # 브랜드 목록
laighthouse-cli brands use 브랜드A          # 기본 브랜드 지정 → 이후 --brand 생략
laighthouse-cli brands access              # 기본 브랜드에 접근할 수 있는 사람과 팀
laighthouse-cli members --all
laighthouse-cli members show kim@example.com
laighthouse-cli invite kim@example.com --role 마케터
laighthouse-cli reports --type daily,mtd
laighthouse-cli reports show <ID> --web    # 차트는 웹에서
laighthouse-cli upload ./sales.xlsx --wait
laighthouse-cli upload history
```

명령 이름 규칙:
- 자주 쓰는 동작은 최상위 동사: `login`, `logout`, `whoami`, `upload`, `invite`
- 동작에 딸린 이력·관리는 그 동사 아래: `upload history`, `invite list`
- 나머지는 명사이고 명사만 쳐도 목록: `brands`, `members`, `teams`, `roles`, `reports`. 단수형(`member`)과 `list`도 받습니다.
- 브랜드·역할·멤버는 ID 대신 이름이나 이메일(일부만 적어도 됨)로 지정할 수 있습니다.

전용 명령이 없는 엔드포인트는 `laighthouse-cli api`로 부릅니다.

```sh
laighthouse-cli api GET /api/authz/me/permissions
laighthouse-cli api POST /api/authz/teams --data '{"name":"그로스팀"}'
```

## 에이전트에서 쓸 때

- `--json`을 붙이면 모든 명령이 API 응답을 그대로 JSON으로 냅니다.
- 각 명령의 `--help`에 기능, 대응하는 API, 필요한 권한이 적혀 있습니다.
- 로그인은 `echo "$PASSWORD" | laighthouse-cli login --username me@laight.ai --password-stdin`.
- 잘못된 하위 명령이나 인자는 비슷한 명령을 제안하고 종료 코드 1로 끝납니다.
- 환경변수: `LAIGHTHOUSE_ENV`(dev|prod), `LAIGHTHOUSE_BRAND`(기본 브랜드), `LAIGHTHOUSE_CONFIG_DIR`.

## 토큰

`login`은 기본으로 브라우저를 열어 웹과 같은 화면에서 로그인합니다(디바이스 코드 플로우). Google/Microsoft로만 가입한 계정도 이 방식으로 로그인할 수 있습니다. 브라우저가 없는 곳에서는 `--no-browser`로 주소와 코드만 받아 다른 기기에서 승인합니다. 인증 서비스에 CLI 클라이언트가 아직 등록되지 않은 환경에서는 `--password`를 쓰라고 안내합니다.

토큰은 `~/.config/laighthouse/credentials.json`(소유자만 읽기)에 환경별로 저장됩니다. 만료 전에 자동 갱신하고, 갱신도 실패하면 다시 로그인하라고 안내합니다. `logout`은 서버 쪽 로그인 세션도 끝냅니다.

## 개발

```sh
make build      # bin/laighthouse-cli
make test
make gen SPEC=<openapi.json 경로>   # 백엔드 스펙에서 클라이언트 재생성
```
