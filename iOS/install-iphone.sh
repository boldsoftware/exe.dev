#!/bin/bash
set -e

cd "$(dirname "$0")"

APP=exe.dev
DEVICE=00008150-00010C8C36FB801C

xcodebuild \
  -scheme $APP \
  -configuration Debug \
  -destination "id=$DEVICE" \
  -derivedDataPath build \
  build

ios-deploy --bundle build/Build/Products/Debug-iphoneos/$APP.app --justlaunch
