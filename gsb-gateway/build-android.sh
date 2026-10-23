#!/usr/bin/env bash
# Builds the Android APK for arm64 and x86_64: the Rust core (core-mobile,
# a JNI library) and the Go bridge/panel (control, run by the app as a child
# process and shipped as libgsbgateway.so so Android extracts it executable),
# both with the NDK's clang, then the app with Gradle. Output:
# ../GSB_Gateway-android.apk. Needs ANDROID_HOME (SDK with ndk;28.2.13676358),
# JAVA_HOME (JDK 17–21) and `rustup target add aarch64-linux-android x86_64-linux-android`.
#   bash gsb-gateway/build-android.sh
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
root="$(dirname "$here")"
: "${ANDROID_HOME:?ANDROID_HOME must point to the Android SDK}"
ndk="${ANDROID_NDK_HOME:-$ANDROID_HOME/ndk/28.2.13676358}"
api=26 # = minSdk in mobile/android/app/build.gradle.kts

case "$(uname -s)" in
  MINGW*|MSYS*|CYGWIN*) host=windows-x86_64; ext=.cmd ;;
  Darwin) host=darwin-x86_64; ext= ;;
  *) host=linux-x86_64; ext= ;;
esac
bin="$ndk/toolchains/llvm/prebuilt/$host/bin"
jni="$here/mobile/android/app/src/main/jniLibs"

cp "$root/ui.html" "$root/mini.html" "$here/control/web/"

build() { # <rust target> <android abi> <clang prefix> <goarch>
  local env_target
  env_target="$(echo "$1" | tr 'a-z-' 'A-Z_')"
  export "CARGO_TARGET_${env_target}_LINKER=$bin/$3$api-clang$ext"
  export "CC_${1//-/_}=$bin/$3$api-clang$ext"
  export "AR_${1//-/_}=$bin/llvm-ar${ext:+.exe}"
  (cd "$here/core-mobile" && cargo build --release --target "$1")
  mkdir -p "$jni/$2"
  cp "$here/core-mobile/target/$1/release/libgsbcore.so" "$jni/$2/"
  # cgo so Go resolves names through bionic (Android has no /etc/resolv.conf).
  (cd "$here/control" && GOOS=android GOARCH="$4" CGO_ENABLED=1 CC="$bin/$3$api-clang$ext"     go build -trimpath -ldflags '-s -w' -o "$jni/$2/libgsbgateway.so" .)
}
build aarch64-linux-android arm64-v8a aarch64-linux-android arm64
build x86_64-linux-android x86_64 x86_64-linux-android amd64

cd "$here/mobile/android"
if [ "$host" = windows-x86_64 ]; then ./gradlew.bat --no-daemon assembleRelease; else ./gradlew --no-daemon assembleRelease; fi
cp app/build/outputs/apk/release/app-release.apk "$root/GSB_Gateway-android.apk"
echo "apk hazir: $root/GSB_Gateway-android.apk"
