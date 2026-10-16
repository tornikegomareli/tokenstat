#!/bin/sh
# Installs the latest tokenstat release:
#   curl -fsSL https://raw.githubusercontent.com/tornikegomareli/tokenstat/main/install.sh | sh
# Set TOKENSTAT_INSTALL_DIR to install somewhere other than ~/.local/bin.
set -eu

repo="tornikegomareli/tokenstat"
dir="${TOKENSTAT_INSTALL_DIR:-$HOME/.local/bin}"
base="${TOKENSTAT_BASE_URL:-https://github.com/$repo/releases/latest/download}"

fail() {
	echo "tokenstat: $1" >&2
	exit 1
}

os=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$os" in
darwin | linux) ;;
*) fail "$os is not supported here. On Windows, use install.ps1 from PowerShell." ;;
esac

case "$(uname -m)" in
x86_64 | amd64) arch=amd64 ;;
arm64 | aarch64) arch=arm64 ;;
*) fail "$(uname -m) is not supported. Releases exist for x86-64 and arm64." ;;
esac

archive="tokenstat_${os}_${arch}.tar.gz"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

echo "Downloading $archive"
curl -fsSL "$base/$archive" -o "$tmp/$archive" || fail "could not download $base/$archive"
curl -fsSL "$base/checksums.txt" -o "$tmp/checksums.txt" || fail "could not download the checksums"

want=$(awk -v f="$archive" '$2 == f { print $1 }' "$tmp/checksums.txt")
if command -v sha256sum >/dev/null 2>&1; then
	got=$(sha256sum "$tmp/$archive" | awk '{ print $1 }')
else
	got=$(shasum -a 256 "$tmp/$archive" | awk '{ print $1 }')
fi
[ -n "$want" ] && [ "$want" = "$got" ] || fail "the checksum of $archive does not match. Nothing was installed."

tar -xzf "$tmp/$archive" -C "$tmp" tokenstat
mkdir -p "$dir"
mv "$tmp/tokenstat" "$dir/tokenstat"
chmod 755 "$dir/tokenstat"
echo "Installed tokenstat to $dir/tokenstat"

# Now that the new one is in place, remove other copies on the PATH, such
# as ~/go/bin/tokenstat from go install: one that comes first would run
# instead. Relative PATH entries depend on the current folder, and symlinks
# usually belong to a package manager, so those are only reported.
real_dir=$(cd "$dir" && pwd -P)
set -f
old_ifs=$IFS
IFS=:
for p in $PATH; do
	case "$p" in
	/*) ;;
	*) continue ;;
	esac
	f="$p/tokenstat"
	[ -f "$f" ] || continue
	real_p=$(cd "$p" 2>/dev/null && pwd -P) || continue
	[ "$real_p" = "$real_dir" ] && continue
	if [ -L "$f" ]; then
		echo "Another tokenstat is on your PATH: $f (a link, maybe from a package manager). Remove it if it is an old one."
	elif rm -f "$f" 2>/dev/null && [ ! -e "$f" ]; then
		echo "Removed another tokenstat: $f"
	else
		echo "Could not remove another tokenstat: $f
It may run instead of this one. Remove it with: sudo rm \"$f\""
	fi
done
IFS=$old_ifs
set +f

case ":$PATH:" in
*":$dir:"* | *":$dir/:"*)
	echo "Run: tokenstat (open a new terminal if an old one still starts)" ;;
*) echo "$dir is not on your PATH. Add it, then run tokenstat:
  export PATH=\"$dir:\$PATH\"" ;;
esac
