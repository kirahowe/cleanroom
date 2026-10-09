#!/bin/sh
# Download the Mailpit binary into .tools/. Mailpit is a local mail server
# for testing: it accepts SMTP and shows what it received in a web inbox.
# Nothing it receives leaves the machine.
set -eu

VERSION=v1.31.4
os=$(uname -s | tr '[:upper:]' '[:lower:]')
arch=$(uname -m)
case "$arch" in
	x86_64) arch=amd64 ;;
	aarch64) arch=arm64 ;;
esac

url="https://github.com/axllent/mailpit/releases/download/$VERSION/mailpit-$os-$arch.tar.gz"
mkdir -p .tools
echo "downloading $url"
curl -fsSL "$url" | tar -xz -C .tools mailpit
.tools/mailpit version
