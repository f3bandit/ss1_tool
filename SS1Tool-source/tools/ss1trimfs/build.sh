#!/bin/sh
# Builds ss1trimfs for the SuperStation (32-bit ARM Linux, static) into ../../bin/ss1trimfs_arm.
# Needs arm-linux-gnueabihf-gcc (Debian/Ubuntu: gcc-arm-linux-gnueabihf).
set -e
cd "$(dirname "$0")"
CC=${CC:-arm-linux-gnueabihf-gcc}
$CC -O2 -std=gnu11 -Wall -static -s -o ../../bin/ss1trimfs_arm ss1trimfs.c
echo "Built bin/ss1trimfs_arm"
