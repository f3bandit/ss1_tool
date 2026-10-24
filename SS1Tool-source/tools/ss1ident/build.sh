#!/bin/sh
# Builds ss1ident for the SuperStation (32-bit ARM Linux, static) into ../../bin/ss1ident_arm.
# Needs arm-linux-gnueabihf-gcc (Debian/Ubuntu: gcc-arm-linux-gnueabihf).
set -e
cd "$(dirname "$0")"
CC=${CC:-arm-linux-gnueabihf-gcc}
L=libchdr
$CC -O2 -static -s -w -o ../../bin/ss1ident_arm ss1ident.c $L/unity.c $L/src/libchdr_codec_avhuff.c \
	-I$L/include -I$L -I$L/deps/miniz-3.1.2 -I$L/deps/lzma-26.02/include -I$L/deps/zstd-1.5.7 \
	-DZ7_ST -D_7ZIP_ST -DZSTD_DISABLE_ASM -lm
echo "Built bin/ss1ident_arm"
