#!/usr/bin/env python3
"""api/openapi.json에서 CLI가 쓰는 operationId 목록을 뽑아 oapi-codegen.yaml을 다시 쓴다.

api/openapi.json도 그 operation과 거기서 참조하는 스키마만 남기고 다시 쓴다. 공개 저장소라서
CLI가 부르지 않는 엔드포인트는 싣지 않는다. 설명(description, summary)에는 백엔드 내부 설계 메모가
섞여 있어 모두 지우고, 열거형에서 운영자 권한 키(platform.*)도 뺀다. 이미 잘라 낸 파일에 다시 돌려도
결과가 같다.

report 태그 전체를 넣으면 차트 스키마 수백 개가 딸려 오므로, report는 목록·메타 조회용 몇 개만
EXTRA_OPERATION_IDS로 고른다. 사용법: scripts/update-openapi.sh
"""

import json
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent

TAGS = {
    "authz",
    "authz-invites",
    "authz-teams",
    "authz-roles",
    "authz-bindings",
    "brands",
    "data-uploads",
    "media-connections",
}
EXTRA_OPERATION_IDS = {
    "get_report",
    "get_report_unread_count",
    "patch_report_published",
    "post_report_read",
    "get_daily_report_status",
    "get_mtd_report_status",
    "get_monthly_report_status",
    "get_dashboard_report_status",
}

HEAD = """\
# 백엔드의 openapi.json에서 타입과 클라이언트를 생성한다. `make gen SPEC=<openapi.json 경로>`.
# 이 파일은 scripts/gen-config.py가 만든다. 직접 고치지 말고 그 스크립트의 TAGS/EXTRA_OPERATION_IDS를 고친다.
# CLI가 쓰는 엔드포인트만 operationId로 고른다. 나머지는 `laighthouse-cli api` 범용 명령으로 부른다.
package: api
output: internal/api/gen.go
generate:
  models: true
  client: true
output-options:
  # 스키마 BrandPermissionsResponse와 brand_permissions 응답 래퍼 이름이 겹쳐 접미사를 바꾼다.
  response-type-suffix: Result
  include-operation-ids:
"""


def refs(node, out: set[str]) -> None:
    """node 안의 $ref("#/components/<종류>/<이름>")를 모두 out에 모은다."""
    if isinstance(node, dict):
        ref = node.get("$ref")
        if isinstance(ref, str):
            out.add(ref)
        for v in node.values():
            refs(v, out)
    elif isinstance(node, list):
        for v in node:
            refs(v, out)


def strip_docs(node) -> None:
    """operation과 스키마의 description, summary를 지운다. 속성 이름이 description인 경우는 남긴다."""
    if isinstance(node, dict):
        for key in ("description", "summary"):
            if isinstance(node.get(key), str):
                del node[key]
        for k, v in node.items():
            if k == "properties" and isinstance(v, dict):
                for prop in v.values():
                    strip_docs(prop)
            else:
                strip_docs(v)
    elif isinstance(node, list):
        for v in node:
            strip_docs(v)


def strip_operator_keys(node) -> None:
    """열거형에서 운영자 권한 키(platform.*)를 뺀다. 공개 CLI는 이 키를 쓰지 않는다."""
    if isinstance(node, dict):
        enum = node.get("enum")
        if isinstance(enum, list) and any(isinstance(v, str) and v.startswith("platform.") for v in enum):
            node["enum"] = [v for v in enum if not (isinstance(v, str) and v.startswith("platform."))]
        for v in node.values():
            strip_operator_keys(v)
    elif isinstance(node, list):
        for v in node:
            strip_operator_keys(v)


def trim(spec: dict, ids: set[str]) -> None:
    """spec에서 ids에 없는 operation과, 남은 operation이 참조하지 않는 컴포넌트를 지운다."""
    paths = {}
    for path, ops in spec["paths"].items():
        kept = {m: op for m, op in ops.items() if not isinstance(op, dict) or op.get("operationId") in ids}
        if any(isinstance(op, dict) and "operationId" in op for op in kept.values()):
            paths[path] = kept
    spec["paths"] = paths

    comps = spec["components"]
    seen: set[str] = set()
    todo: set[str] = set()
    refs(paths, todo)
    while todo:
        ref = todo.pop()
        if ref in seen:
            continue
        seen.add(ref)
        _, _, kind, name = ref.split("/", 3)
        refs(comps[kind][name], todo)
    for kind, items in comps.items():
        if kind == "securitySchemes":
            continue
        comps[kind] = {n: v for n, v in items.items() if f"#/components/{kind}/{n}" in seen}


def main() -> None:
    spec = json.loads((ROOT / "api" / "openapi.json").read_text(encoding="utf-8"))
    ids: set[str] = set()
    for ops in spec["paths"].values():
        for op in ops.values():
            if not isinstance(op, dict):
                continue
            if TAGS & set(op.get("tags", [])) or op["operationId"] in EXTRA_OPERATION_IDS:
                ids.add(op["operationId"])
    trim(spec, ids)
    strip_docs(spec)
    strip_operator_keys(spec)
    spec["info"] = {"title": "laighthouse API", "version": spec["info"]["version"]}
    (ROOT / "api" / "openapi.json").write_text(
        json.dumps(spec, ensure_ascii=False, indent=2, sort_keys=True) + "\n", encoding="utf-8"
    )
    body = "".join(f"    - {i}\n" for i in sorted(ids))
    (ROOT / "oapi-codegen.yaml").write_text(HEAD + body, encoding="utf-8")
    print(f"{len(ids)} operation ids")


if __name__ == "__main__":
    main()
