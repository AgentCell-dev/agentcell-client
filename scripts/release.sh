#!/bin/sh
set -eu

version=${1:?version required}
out=${2:-dist}
case "$version" in *[!0-9A-Za-z._-]*) echo "invalid version"; exit 2;; esac

go_version=$(go env GOVERSION)
test "$go_version" = "go1.25.5" || { echo "release requires go1.25.5, got $go_version"; exit 2; }
mkdir -p "$out"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT HUP INT TERM

for target in darwin/amd64 darwin/arm64 linux/amd64 linux/arm64 windows/amd64 windows/arm64; do
	os=${target%/*}; arch=${target#*/}; suffix=""; test "$os" = windows && suffix=.exe
	name="agentcell_${version}_${os}_${arch}${suffix}"
	for pass in one two; do
		CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath -buildvcs=false \
			-ldflags "-s -w -buildid= -X main.version=$version" -o "$tmp/$name.$pass" ./cmd/agentcell
	done
	cmp "$tmp/$name.one" "$tmp/$name.two" || { echo "non-reproducible output: $name"; exit 1; }
	cp "$tmp/$name.one" "$out/$name"
done

(cd "$out" && shasum -a 256 agentcell_* > SHA256SUMS)
echo "PASS: reproducible binaries and SHA256SUMS written to $out"

