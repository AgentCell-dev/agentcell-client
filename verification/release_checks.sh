#!/bin/sh
set -eu

client=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)
real_go=$(command -v go)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT HUP INT TERM

echo "--- rebuild existing 0.0.0-test artefacts ---"
(cd "$client" && scripts/release.sh 0.0.0-test "$tmp/rebuilt")
cmp "$client/dist/SHA256SUMS" "$tmp/rebuilt/SHA256SUMS"
(cd "$tmp/rebuilt" && shasum -a 256 -c "$client/dist/SHA256SUMS")
echo "PASS rebuilt checksums are byte-for-byte identical to existing SHA256SUMS"

echo "--- perturb every .two build and require cmp rejection ---"
mkdir -p "$tmp/perturb-bin"
cp "$(dirname "$0")/wrappers/perturb-go" "$tmp/perturb-bin/go"
chmod +x "$tmp/perturb-bin/go"
set +e
perturbed=$(cd "$client" && REAL_GO="$real_go" PATH="$tmp/perturb-bin:$PATH" scripts/release.sh 0.0.0-negative "$tmp/perturbed" 2>&1)
perturbed_status=$?
set -e
test "$perturbed_status" -ne 0
printf '%s\n' "$perturbed" | grep 'non-reproducible output:' >/dev/null
printf '%s\n' "$perturbed"
echo "PASS release rejected intentionally different pass-two bytes"

echo "--- spoof a different toolchain and require pre-build rejection ---"
mkdir -p "$tmp/version-bin"
cp "$(dirname "$0")/wrappers/wrong-version-go" "$tmp/version-bin/go"
chmod +x "$tmp/version-bin/go"
set +e
versioned=$(cd "$client" && REAL_GO="$real_go" PATH="$tmp/version-bin:$PATH" scripts/release.sh 0.0.0-negative "$tmp/wrong-version" 2>&1)
version_status=$?
set -e
test "$version_status" -eq 2
printf '%s\n' "$versioned" | grep 'release requires go1.25.5, got go1.25.4' >/dev/null
printf '%s\n' "$versioned"
echo "PASS release rejected the wrong Go version before building"
