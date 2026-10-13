#!/bin/sh
# Download the Mailpit binary into .tools/. Mailpit is a local mail server
# for testing: it accepts SMTP and shows what it received in a web inbox.
# Nothing it receives leaves the machine.
#
# The download is checked against a SHA-256 pinned below before anything is
# unpacked, so a release that changes after the fact is not run. To move to
# a new version, change VERSION and take the digests from the release's
# asset list on GitHub.
set -eu

VERSION=v1.31.4
os=$(uname -s | tr '[:upper:]' '[:lower:]')
arch=$(uname -m)
case "$arch" in
	x86_64) arch=amd64 ;;
	aarch64) arch=arm64 ;;
esac

case "$os-$arch" in
	darwin-arm64) want=26afeae329874fa6154dd83bd2e0f307e5df858054145dc7e98ec1aa426a8381 ;;
	darwin-amd64) want=9ff1d1b249ccb884ca48a6fca01f69f9db88b07692dd98e115294778a0b20f54 ;;
	linux-amd64) want=30942c4605c2ca8b9f759b1bb4e3ab6a12bdfdf66e5c94644ec2c75ac41e88e7 ;;
	linux-arm64) want=010a346a8b9d454f6fa22d4fd2dbd5bf5eb7104688412b9c130d0ba33a3579c4 ;;
	*)
		echo "no pinned Mailpit download for $os-$arch: install mailpit yourself and put it on PATH" >&2
		exit 1
		;;
esac

url="https://github.com/axllent/mailpit/releases/download/$VERSION/mailpit-$os-$arch.tar.gz"
mkdir -p .tools
archive=.tools/mailpit.tar.gz
trap 'rm -f "$archive"' EXIT
echo "downloading $url"
curl -fsSL -o "$archive" "$url"

if command -v sha256sum >/dev/null 2>&1; then
	got=$(sha256sum "$archive" | cut -d' ' -f1)
else
	got=$(shasum -a 256 "$archive" | cut -d' ' -f1)
fi
if [ "$got" != "$want" ]; then
	echo "checksum mismatch for $url" >&2
	echo "  expected $want" >&2
	echo "  got      $got" >&2
	echo "nothing was unpacked" >&2
	exit 1
fi

tar -xz -C .tools -f "$archive" mailpit
.tools/mailpit version
