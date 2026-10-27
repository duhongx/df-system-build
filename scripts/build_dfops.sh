#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
stage="$(mktemp -d "${TMPDIR:-/tmp}/dfops-build.XXXXXX")"
cleanup() { python3 -c 'import shutil,sys; shutil.rmtree(sys.argv[1], ignore_errors=True)' "$stage"; }
trap cleanup EXIT

target_os="${DFOPS_GOOS:-linux}"
target_arch="${DFOPS_GOARCH:-amd64}"
build_cc="${CC:-}"
if [[ -z "$build_cc" && "$(go env GOHOSTOS)" != "$target_os" ]]; then
  if [[ "$target_os/$target_arch" != "linux/amd64" ]] || ! command -v zig >/dev/null; then
    echo 'Cross-compiling CGO requires CC, or Zig for linux/amd64.' >&2
    exit 1
  fi
  printf '#!/usr/bin/env bash\nexec %q cc -target x86_64-linux-gnu "$@"\n' "$(command -v zig)" > "$stage/cc"
  chmod +x "$stage/cc"
  build_cc="$stage/cc"
fi
if [[ -z "$build_cc" ]]; then build_cc="$(go env CC)"; fi

cd "$repo_root/frontend"
npm run build -- --outDir "$stage/frontend" --emptyOutDir
python3 - "$repo_root" "$stage" <<'PY'
from pathlib import Path
import shutil, sys
repo, stage = map(Path, sys.argv[1:])
# Also refresh the source embed entry point for ordinary local go builds.
# Retain prior generated files; the customer binary uses only this fresh dist.
shutil.copytree(stage / 'frontend', repo / 'backend/internal/web/dist', dirs_exist_ok=True)
shutil.copytree(repo / 'backend', stage / 'backend', ignore=shutil.ignore_patterns('dist', 'bin'))
shutil.copytree(stage / 'frontend', stage / 'backend/internal/web/dist')
PY

cd "$stage/backend"
# Check the frontend actually embedded by Go, including the route referenced
# by index.html, before producing a customer binary.
go test ./internal/web -run 'TestEmbeddedSQLExecutionActions|TestEmbeddedResumableArtifactUpload|TestEmbeddedOperationalFixes|TestSPACacheAndMissingAssets' -count=1
CGO_ENABLED=1 GOOS="$target_os" GOARCH="$target_arch" CC="$build_cc" \
  go build -trimpath -ldflags='-s -w' -o "$stage/dfops" ./cmd/server

python3 - "$repo_root" "$stage" <<'PY'
from pathlib import Path
import os, shutil, sys
repo, stage = map(Path, sys.argv[1:])
dest = repo / 'output/dfops'
dest.parent.mkdir(parents=True, exist_ok=True)
pending = dest.with_name('dfops.new')
shutil.copyfile(stage / 'dfops', pending)
pending.chmod(0o755)
os.replace(pending, dest)
print(dest)
PY
file "$repo_root/output/dfops"
shasum -a 256 "$repo_root/output/dfops"
