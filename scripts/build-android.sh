#!/bin/sh
# build-android.sh ARCH OUT [LDFLAGS] — build the Termux / Android binary for
# ARCH (arm64 | armv7) to OUT, then verify it with check-android.sh.
#
# The Termux builds are GOOS=android, not GOOS=linux (#1230). Android puts
# every app, Termux included, under a seccomp filter that kills the process
# with SIGSYS on a blocked system call, and faccessat2 is blocked on every
# Android version (bionic/libc/SECCOMP_BLOCKLIST_APP.TXT). Go's
# syscall.Faccessat tries faccessat2 first on GOOS=linux and skips it only on
# GOOS=android, and os/exec.LookPath reaches it whenever the program it looks
# for exists, so a GOOS=linux binary died on start-up on any phone with the
# Termux:API package installed (atotto/clipboard looks up
# termux-clipboard-set in its init). GOOS=android also gets Go's other Android
# accommodations, such as the bionic DNS resolver.
#
# android/arm can only be linked externally, so both arches build with cgo
# against the Android NDK. The result links the Android system libraries
# (libc.so, libdl.so, liblog.so) through /system/bin/linker{,64}; API 21
# (Android 5.0) is the floor. -tags nolibasound keeps the purego ALSA
# backend out: Android has no libasound.
#
# The NDK is found through ANDROID_NDK_HOME, ANDROID_NDK_LATEST_HOME (preset
# on GitHub's ubuntu runners) or ANDROID_NDK_ROOT.
set -eu
arch=${1:?usage: build-android.sh ARCH OUT [LDFLAGS]}
out=${2:?usage: build-android.sh ARCH OUT [LDFLAGS]}
ldflags=${3:-}
api=21

ndk=${ANDROID_NDK_HOME:-${ANDROID_NDK_LATEST_HOME:-${ANDROID_NDK_ROOT:-}}}
if [ -z "$ndk" ] || [ ! -d "$ndk" ]; then
	echo "build-android: Android NDK not found; set ANDROID_NDK_HOME (https://developer.android.com/ndk/downloads)" >&2
	exit 2
fi
case "$(uname -s)" in
Linux) host=linux-x86_64 ;;
Darwin) host=darwin-x86_64 ;;
*) echo "build-android: unsupported build host $(uname -s)" >&2; exit 2 ;;
esac
bin="$ndk/toolchains/llvm/prebuilt/$host/bin"

case "$arch" in
arm64) goarch=arm64; goarm= ; cc="$bin/aarch64-linux-android$api-clang" ;;
armv7) goarch=arm; goarm=7; cc="$bin/armv7a-linux-androideabi$api-clang" ;;
*) echo "build-android: unknown ARCH $arch (want arm64 or armv7)" >&2; exit 2 ;;
esac
[ -x "$cc" ] || { echo "build-android: $cc not found in the NDK" >&2; exit 2; }

echo "  → GOOS=android GOARCH=$goarch${goarm:+ GOARM=$goarm} CGO_ENABLED=1 CC=$(basename "$cc") -tags nolibasound"
CGO_ENABLED=1 CC="$cc" GOOS=android GOARCH="$goarch" GOARM="$goarm" \
	${GO:-go} build -trimpath -tags "nolibasound ${TAGS:-}" -ldflags "$ldflags" -o "$out" ./cmd/gophertrunk
"$(dirname "$0")/check-android.sh" "$out"
