#!/usr/bin/env bash
# check-anon.sh —— 匿名与隐私守护（随仓库分发，CI 强制）。
#
# 本脚本只做**结构校验**，不内嵌任何敏感词：
# 匿名身位是占位值、邮箱按形态判定、敏感词表从仓外注入。
# 把「我们在藏什么」写进被守护的仓库，等于公开它——那是泄漏源。
#
# 校验线：
#   1. 我们的提交身位必须是占位值（author 与 committer 都要）
#   2. 树与提交信息不得出现真实邮箱（形态判定，无需点名）
#   3. 敏感词表从 $NORMA_REDACT_TERMS（逗号分隔）或 $NORMA_REDACT_TERMS_FILE
#      注入；两者都未设置时跳过本项并在输出里显式提示「未启用」
#   4. tag 身位只点名不阻断
#
# 红线目的是保护**我方与公司隐私**；上游贡献者的公开署名属于 Apache-2.0
# §4(a) 的 attribution，保留它是合规义务，不在红线内。
#
# 实现注意：所有汇总判断在主 shell 内做，**不用管道包 while**——管道会把
# while 丢进子 shell，fail=1 传不回来（会导致「有 ✗ 却报通过」）。
set -euo pipefail

ANON_NAME="${ANON_NAME:-dev}"
ANON_EMAIL="${ANON_EMAIL:-dev@localhost}"
UPSTREAM_REF="${UPSTREAM_REF:-}"
if [ -z "$UPSTREAM_REF" ] && git rev-parse --verify -q upstream/main >/dev/null; then
  UPSTREAM_REF="upstream/main"
fi
BASE_REF="${BASE_REF:-v0.4.3}"
OURS=${UPSTREAM_REF:-$BASE_REF}

fail=0
note() { printf '  %s\n' "$*"; }
err()  { printf '  ✗ %s\n' "$*"; fail=1; }
warn() { printf '  ⚠ %s\n' "$*"; }

# 允许的邮箱形态：本地占位 + RFC 2606 保留域 + GitHub noreply。
# 其余一律视为真实邮箱——判定靠形态，不靠点名。
allowed_email() {
  case "$1" in
    dev@localhost|*@localhost)      return 0 ;;
    *@example.com|*@example.org)    return 0 ;;
    *@example.net|*@example.invalid) return 0 ;;
    *@users.noreply.github.com)     return 0 ;;
    *)                              return 1 ;;
  esac
}

echo "── 1) 我们的提交身位（判定基准: $OURS）"
bad=$(git log "$OURS..HEAD" --format='%an|%ae|%cn|%ce|%s' || true)
if [ -n "$bad" ]; then
  while IFS= read -r l; do
    [ -z "$l" ] && continue
    an=${l%%|*}; rest=${l#*|}; ae=${rest%%|*}; rest=${rest#*|}
    cn=${rest%%|*}; rest=${rest#*|}; ce=${rest%%|*}
    if [ "$an" != "$ANON_NAME" ] || [ "$ae" != "$ANON_EMAIL" ] || [ "$cn" != "$ANON_NAME" ] || [ "$ce" != "$ANON_EMAIL" ]; then
      err "身位不符: $l"
    fi
  done <<< "$bad"
  [ "$fail" -eq 0 ] && note "✓ $(git rev-list --count "$OURS..HEAD") 个我方提交全部匿名"
else
  note "· 无新增提交"
fi

echo "── 2) 真实邮箱形态（树 + 提交信息；形态判定，不点名）"
mails=$(
  {
    git grep -oh -E '[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}' HEAD -- \
      '*.go' '*.md' '*.json' '*.yaml' '*.yml' '*.sh' 2>/dev/null || true
    git log "$OURS..HEAD" --format='%B' 2>/dev/null | \
      grep -oh -E '[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}' || true
  } | sort -u
)
while IFS= read -r m; do
  [ -z "$m" ] && continue
  if ! allowed_email "$m"; then
    err "真实邮箱: $m"
  fi
done <<< "$mails"

echo "── 3) 敏感词表（仓外注入）"
terms_src=""
if [ -n "${NORMA_REDACT_TERMS:-}" ]; then
  terms_src="env"
elif [ -n "${NORMA_REDACT_TERMS_FILE:-}" ] && [ -f "${NORMA_REDACT_TERMS_FILE}" ]; then
  terms_src="file:${NORMA_REDACT_TERMS_FILE}"
fi
if [ -z "$terms_src" ]; then
  warn "未启用：NORMA_REDACT_TERMS / NORMA_REDACT_TERMS_FILE 均未设置（词表永不入库，须由 CI secret 或操作者注入）"
else
  if [ "$terms_src" = "env" ]; then
    IFS=',' read -r -a termlist <<< "${NORMA_REDACT_TERMS}"
  else
    mapfile -t termlist < "${NORMA_REDACT_TERMS_FILE}"
  fi
  pat=""
  for t in "${termlist[@]}"; do
    t=$(printf '%s' "$t" | sed 's/^[[:space:]]*//;s/[[:space:]]*$//')
    [ -z "$t" ] && continue
    if [ -z "$pat" ]; then pat="$t"; else pat="$pat|$t"; fi
  done
  if [ -n "$pat" ]; then
    hits=$(git grep -n -i -E "$pat" HEAD -- '*.go' '*.md' '*.json' '*.yaml' '*.yml' '*.sh' 2>/dev/null || true)
    if [ -n "$hits" ]; then
      while IFS= read -r l; do [ -n "$l" ] && err "敏感词: $l"; done <<< "$hits"
    else
      note "✓ 树内容干净（词表 ${#termlist[@]} 条，来源 $terms_src）"
    fi
    mhits=$(git log "$OURS..HEAD" --format='%B' | grep -n -i -E "$pat" || true)
    if [ -n "$mhits" ]; then
      while IFS= read -r l; do [ -n "$l" ] && err "提交信息: $l"; done <<< "$mhits"
    else
      note "✓ 提交信息干净"
    fi
  fi
fi

echo "── 4) tag 身位（只点名）"
tags=$(git for-each-ref --format='%(refname:short)|%(taggeremail)' refs/tags 2>/dev/null || true)
while IFS= read -r l; do
  [ -z "$l" ] && continue
  nm=${l%%|*}; ml=${l##*|}
  if [ -z "$ml" ] || [ "$ml" = "$ANON_EMAIL" ]; then
    note "· $nm  [${ml:-未签名}]"
  elif allowed_email "$ml"; then
    note "· $nm  [占位/noreply]"
  else
    warn "· $nm  [第三方署名: $ml]"
  fi
done <<< "$tags"

echo
if [ "$fail" -ne 0 ]; then
  echo "✗ 隐私红线未通过"
  exit 1
fi
echo "✓ 全部通过"
