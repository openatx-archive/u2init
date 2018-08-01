#!/bin/bash -
#

set -e

# Verisons modify here
ATX_AGENT_VERSION=0.3.6
UIAUTOMATOR_APK_VERSION=1.1.4
STF_BINARIES_VERSION=0.1

RESDIR="resources"
# Download resources
mkdir -p $RESDIR

GITHUB_URL="https://github.com"
GITHUB_URL="https://github-mirror.open.netease.com"

# https://github.com/openatx/stf-binaries/archive/0.1.zip

download_apk(){
	APK_BASE_URL="$GITHUB_URL/openatx/android-uiautomator-server/releases/download/${UIAUTOMATOR_APK_VERSION}"
	wget -O $RESDIR/app-uiautomator.apk "$APK_BASE_URL/app-uiautomator.apk"
	wget -O $RESDIR/app-uiautomator-test.apk "$APK_BASE_URL/app-uiautomator-test.apk"
}

download_stf(){
	## minicap+minitouch
	wget -O $RESDIR/stf-binaries.zip "$GITHUB_URL/openatx/stf-binaries/archive/$STF_BINARIES_VERSION.zip"
	unzip -o -d $RESDIR/ $RESDIR/stf-binaries.zip
}

download_atx(){
	## atx-agent
	wget -O $RESDIR/atx-agent-$ATX_AGENT_VERSION.tar.gz "$GITHUB_URL/openatx/atx-agent/releases/download/$ATX_AGENT_VERSION/atx-agent_${ATX_AGENT_VERSION}_linux_armv6.tar.gz"
	tar -C $RESDIR/ -xzvf $RESDIR/atx-agent-$ATX_AGENT_VERSION.tar.gz atx-agent
}

download_atx
download_apk
download_stf

echo "Everything is downloaded. ^_^"
