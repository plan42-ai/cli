#!/bin/bash

set -e

binaries=(
    "plan42"
    "plan42-runner"
    "plan42-runner-config"
)
VERSION=$1
arch=$(uname -m)
os=$(uname | tr '[:upper:]' '[:lower:]')

if [[ "$os" == "darwin" ]]; then
  mkdir -p "dist/plan42-${VERSION}-macos-${arch}/bin"
  cp -a "${binaries[@]}" "dist/plan42-${VERSION}-macos-${arch}/bin"
  tar -czf "dist/plan42-${VERSION}-macos-${arch}.tgz" -C "dist" "plan42-${VERSION}-macos-${arch}"
  rm -rf "dist/plan42-${VERSION}-macos-${arch}"
elif [[ "$os" == "linux" ]]; then
  rm -rf *.deb
  mkdir -p "dist/plan42-${VERSION}-linux-${arch}/deb_tmp"
  cp -a "${binaries[@]}" "dist/plan42-${VERSION}-linux-${arch}/deb_tmp"
  pushd dist && fpm -C "plan42-${VERSION}-linux-${arch}/deb_tmp" \
     -s dir \
     -t deb \
     -n plan42-cli \
     -v $VERSION \
     --maintainer "Plan42.ai <founders@plan42.ai>" \
     --prefix /usr/bin
  popd
  rm -rf "dist/plan42-${VERSION}-linux-${arch}"
fi