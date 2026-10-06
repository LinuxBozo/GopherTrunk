#!/bin/sh
# check-android.sh BINARY... — fail unless every BINARY is an Android
# executable that can start inside Termux (#1230):
#
#   1. its ELF interpreter is Android's /system/bin/linker or linker64, not
#      glibc's (Android has no /lib/ld-linux*.so);
#   2. it links only Android system libraries (libc.so, libdl.so, liblog.so,
#      libm.so) — a stray libasound.so or libc.so.6 would not resolve;
#   3. it carries no syscall.faccessat2 wrapper. Android's app seccomp filter
#      kills a process with SIGSYS on faccessat2, and Go links the wrapper
#      only on GOOS=linux, so its presence means the binary was built for the
#      wrong OS and will die on the first exec.LookPath of an installed program.
set -eu
command -v readelf >/dev/null || { echo "check-android: readelf not found (install binutils)" >&2; exit 2; }
status=0
for bin in "$@"; do
	ok=1
	if ! readelf -h "$bin" >/dev/null 2>&1; then
		echo "check-android: $bin is not an ELF file" >&2
		ok=0
		continue
	fi
	interp=$(readelf -l "$bin" | sed -n 's/.*Requesting program interpreter: \(.*\)]/\1/p')
	case "$interp" in
	/system/bin/linker | /system/bin/linker64) ;;
	*)
		echo "check-android: $bin has interpreter '${interp:-none (static)}', want /system/bin/linker{,64} (built as GOOS=linux?)" >&2
		ok=0
		;;
	esac
	bad=$(readelf -d "$bin" | sed -n 's/.*(NEEDED).*\[\(.*\)\]/\1/p' | grep -v -x -e libc.so -e libdl.so -e liblog.so -e libm.so || true)
	if [ -n "$bad" ]; then
		echo "check-android: $bin links non-Android libraries: $bad" >&2
		ok=0
	fi
	if ${GO:-go} tool nm "$bin" | grep 'syscall\.faccessat2' >/dev/null; then
		echo "check-android: $bin links syscall.faccessat2, which Android's seccomp filter answers with SIGSYS" >&2
		ok=0
	fi
	if [ $ok -eq 1 ]; then
		echo "check-android: $bin is an Android executable ($interp)"
	else
		status=1
	fi
done
exit $status
