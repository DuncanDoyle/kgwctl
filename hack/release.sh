#!/usr/bin/env bash
# Build the release archives for one tag. Never pushes and never publishes.
#
#   hack/release.sh <tag>                    real build: clean tree, HEAD must be the tag commit;
#                                            writes hack/release/out/<tag>/ and prints the close steps
#   RELEASE_DRY_RUN=1 hack/release.sh <tag>  skips both checks; writes only hack/release/out/dry-run/<tag>/
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TARGETS="linux/amd64 linux/arm64 darwin/amd64 darwin/arm64"

die() { echo "release: $*" >&2; exit 1; }

[ "$#" -eq 1 ] || die "usage: hack/release.sh <tag>"
tag="$1"
# Go module versions take a pre-release suffix but no +build metadata.
[[ "$tag" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]] || die "not a version tag: $tag"

# A git-ignored go.work or a host GOFLAGS must not change what gets built.
# A non-empty GOFLAGS also overrides one set with `go env -w`.
export GOWORK=off GOFLAGS=-mod=readonly

dry_run=false
[ "${RELEASE_DRY_RUN:-}" = "1" ] && dry_run=true

cd "$ROOT"
commit="$(git rev-parse HEAD)"

if $dry_run; then
  rel="hack/release/out/dry-run/$tag"
else
  [ -z "$(git status --porcelain --untracked-files=normal)" ] || die "working tree is not clean"
  # Resolves tags only, so a branch with the same name does not pass.
  tag_commit="$(git rev-parse --verify --quiet "refs/tags/$tag^{commit}")" || die "no tag $tag"
  [ "$tag_commit" = "$commit" ] || die "HEAD $commit is not the commit of tag $tag ($tag_commit)"
  rel="hack/release/out/$tag"
fi

out="$ROOT/$rel"
if $dry_run; then
  rm -rf "$out"
  mkdir -p "$out"
else
  # A plain mkdir claims the directory atomically: of two concurrent builds, one gets it.
  mkdir -p "$(dirname "$out")"
  mkdir "$out" 2>/dev/null || die "$rel exists; remove it to rebuild"
  # shellcheck disable=SC2317,SC2329 # invoked by the EXIT trap
  partial() { [ "$?" -eq 0 ] || echo "release: partial output left in $rel; remove it to rebuild" >&2; }
  trap partial EXIT
fi
stage="$out/.stage"
mkdir -p "$stage"

if command -v sha256sum >/dev/null 2>&1; then
  sha256() { sha256sum "$@"; }
else
  sha256() { shasum -a 256 "$@"; }
fi

host="$(go env GOHOSTOS)/$(go env GOHOSTARCH)"
host_bin=""
archives=""
for target in $TARGETS; do
  os="${target%/*}"
  arch="${target#*/}"
  dir="$stage/${os}_${arch}"
  mkdir -p "$dir"
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath \
    -ldflags "-s -w -X main.version=$tag -X main.commit=$commit" \
    -o "$dir/kgwctl" ./cmd/kgwctl
  cp LICENSE README.md "$dir/"
  archive="kgwctl_${tag}_${os}_${arch}.tar.gz"
  # COPYFILE_DISABLE keeps macOS tar from adding ._ metadata entries.
  COPYFILE_DISABLE=1 tar -czf "$out/$archive" -C "$dir" kgwctl LICENSE README.md
  archives="$archives $archive"
  [ "$target" = "$host" ] && host_bin="$dir/kgwctl"
done

[ -n "$host_bin" ] || die "host $host is not a release target; cannot check the version"
got="$("$host_bin" version --client)"
case "$got" in
  *"kgwctl $tag ($commit,"*) ;;
  *) die "version --client printed '$got', want $tag and $commit" ;;
esac
rm -rf "$stage"

# Name each archive explicitly; never a glob.
# shellcheck disable=SC2086 # $archives is a space-separated list of fixed names
(cd "$out" && sha256 $archives > checksums.txt)
echo "$commit" > "$out/commit.txt"

if $dry_run; then
  echo "dry run: not for release"
  exit 0
fi

: > "$out/publish-files.txt"
for a in $archives; do echo "$rel/$a" >> "$out/publish-files.txt"; done
echo "$rel/checksums.txt" >> "$out/publish-files.txt"

files=""
while IFS= read -r f; do files="$files $f"; done < "$out/publish-files.txt"

cat <<EOF
Built $tag at $commit into $rel/.
Nothing is pushed or published. Close steps, in this order:

1. Push exactly the tag:
   git push origin refs/tags/$tag
2. Check the remote tag resolves to $(cat "$out/commit.txt") (the ^{} line for an annotated tag); stop on a mismatch:
   git ls-remote origin "refs/tags/$tag^{}" "refs/tags/$tag"
3. Check checksums.txt lists exactly the four archives of publish-files.txt, then:
   (cd $rel && shasum -a 256 -c checksums.txt)
4. Clean install into fresh directories, then check 'version --client' prints $tag (retry with GOPROXY=direct if the proxy lacks the tag):
   mkdir -p .agents-tmp && d="\$(mktemp -d "\$PWD/.agents-tmp/release-install.XXXXXX")"
   GOPATH="\$d/gopath" GOMODCACHE="\$d/modcache" GOBIN="\$d/bin" \\
     go install github.com/DuncanDoyle/kgwctl/cmd/kgwctl@$tag
   "\$d/bin/kgwctl" version --client
5. Publish:
   gh release create $tag --verify-tag --title $tag --notes-file <notes-file>$(for f in $files; do printf ' \\\n     %s' "$f"; done)
EOF
