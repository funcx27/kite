#!/usr/bin/env bash
# Lightweight ui/src boundary check (split plan P4), complementing the
# no-restricted-imports rules in ui/eslint-boundaries.js (appended from
# ui/eslint.config.js). Unlike eslint's textual patterns, this resolves
# relative import paths too.
#
# Direction A: src/portal/** (the portal shell plus the four feature modules
#              application/workflow/buildfleet/kcross) must not import
#              src/pages/**, src/components/** (except components/ui/** and
#              the shared allowlist below), or src/App.
# Direction B: host side (App.tsx, pages/, components/, contexts/, lib/) must
#              not import the portal (src/portal/**).
# Direction C: the four portal modules must not import each other except via
#              the cross-module whitelist below.
#
# Exemptions mirror ui/eslint-boundaries.js:
#   - src/portal/main.tsx — portal SPA entry, composition root
#   - SHARED_OK — components verified to be used on both sides
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SRC="$ROOT/ui/src"

MODULE_RE='^portal(/|$)'
PORTAL_MODULE_RE='^portal/(application|workflow|buildfleet|kcross)(/|$)'
HOST_RE='^(App(\.tsx)?$|pages/|components/|contexts/|lib/)'

# Keep in sync with SHARED_COMPONENT_ALLOWS in ui/eslint-boundaries.js.
SHARED_OK='^components/ui(/|$)|^components/(account-settings-dialog|action-table|ai-chat/ai-chat-stream|appearance-provider|chart/cpu-usage-chart|chart/disk-io-usage-chart|chart/memory-usage-chart|chart/network-usage-chart|color-theme-provider|connection-indicator|delete-confirmation-dialog|error-message|language-toggle|mode-toggle|network-speed-indicator|selector/configmap-selector|selector/container-selector|selector/pod-selector|selector/secret-selector|settings/cluster-dialog|simple-yaml-editor|yaml-diff-viewer)$'

# Keep in sync with CROSS_MODULE_WHITELIST in ui/eslint-boundaries.js; the
# reasons for each entry are documented there.
CROSS_MODULE_OK_application='^portal/workflow/api$|^portal/workflow/types$|^portal/workflow/components/(build-providers-management|build-runtime-settings|credentials-management|run-dialogs|space-build-settings|status-chip)$|^portal/kcross/kcross-management$'
CROSS_MODULE_OK_workflow='^portal/application/api$|^portal/application/components/brand-icons$'
CROSS_MODULE_OK_buildfleet=''
CROSS_MODULE_OK_kcross=''

fail=0

# Resolves an import specifier (alias "@/x" or relative) of a src-relative
# file to a src-relative path.
resolve_spec() {
  local relfile="$1" spec="$2" dir
  if [[ "$relfile" == */* ]]; then dir="${relfile%/*}"; else dir=""; fi
  if [[ "$spec" == @/* ]]; then
    printf '%s' "${spec#@/}"
  else
    realpath -m --relative-to="$SRC" "$SRC/${dir:+$dir/}$spec"
  fi
}

check_file() {
  local file="$1" rel="$2" spec resolved srcmod m wl
  # lines that carry an import/export source beginning with "@/"" or "."
  while IFS= read -r hit; do
    local lineno="${hit%%:*}"
    spec="$(printf '%s' "$hit" | grep -oE "['\"][@.][^'\"]*['\"]" | head -n1 | tr -d "'\"")"
    [ -n "$spec" ] || continue
    resolved="$(resolve_spec "$rel" "$spec")"

    if [[ "$rel" =~ $MODULE_RE ]]; then
      # Direction A
      if [[ "$resolved" == App || "$resolved" == App.tsx || "$resolved" == App/* ]] \
        || [[ "$resolved" == pages || "$resolved" == pages/* ]] \
        || { [[ "$resolved" == components || "$resolved" == components/* ]] \
             && ! [[ "$resolved" =~ $SHARED_OK ]]; }; then
        echo "A  $rel:$lineno  ->  $spec  (resolves $resolved)" >&2
        fail=1
      fi
    fi

    if [[ "$rel" =~ $HOST_RE ]] && [[ "$resolved" =~ $MODULE_RE ]]; then
      # Direction B
      echo "B  $rel:$lineno  ->  $spec  (resolves $resolved)" >&2
      fail=1
    fi

    if [[ "$rel" =~ $PORTAL_MODULE_RE ]]; then
      # Direction C: cross-module imports between the four portal modules
      srcmod="${rel#portal/}"
      srcmod="${srcmod%%/*}"
      for m in application workflow buildfleet kcross; do
        [ "$m" = "$srcmod" ] && continue
        if [[ "$resolved" == portal/$m || "$resolved" == portal/$m/* ]]; then
          wl="CROSS_MODULE_OK_${srcmod}"
          if [[ -n "${!wl:-}" ]] && [[ "$resolved" =~ ${!wl} ]]; then
            continue
          fi
          echo "C  $rel:$lineno  ->  $spec  (resolves $resolved)" >&2
          fail=1
        fi
      done
    fi
  done < <(grep -nE "(from|import)[[:space:]]*\(?[[:space:]]*['\"][@.]" "$file" || true)
}

while IFS= read -r file; do
  rel="${file#"$SRC"/}"
  [ "$rel" = "portal/main.tsx" ] && continue # portal SPA entry
  if [[ "$rel" =~ $MODULE_RE ]] || [[ "$rel" =~ $HOST_RE ]]; then
    check_file "$file" "$rel"
  fi
done < <(grep -rlE "(from|import)[[:space:]]*\(?[[:space:]]*['\"][@.]" "$SRC" \
  --include='*.ts' --include='*.tsx' | sort)

if [ "$fail" -ne 0 ]; then
  echo "ui boundary check FAILED (A = portal imports host; B = host imports portal; C = portal modules import each other)" >&2
  exit 1
fi
echo "ui boundary check: clean"
