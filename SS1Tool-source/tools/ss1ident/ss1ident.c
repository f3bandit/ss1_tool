/* ss1ident: what a game file is, worked out on the SuperStation so only the answer crosses the
 * network. SS1 Tool's artwork lookups use it to find a game by its content, not its file name.
 * Copyright (C) 2026 f3bandit. GPL-3.0 (part of SS1 Tool; the method is the same as Phosphor's).
 *
 * Reads lines "<kind>\t<path>" on stdin and prints "<path>\t<crc32>\t<product code>\t<size>" for
 * each (an empty field when it doesn't apply or couldn't be read). kind:
 *   cart  CRC32 of the ROM data the way the No-Intro sets count it: copier headers left out
 *         (iNES/FDS 16 bytes, SNES 512, Atari 7800 128, Lynx 64), N64 read in .z64 byte order
 *   snes  the same, for SNES files (a 512-byte copier header is found by the size)
 *   psx   PlayStation disc (.cue/.bin/.iso/.chd): the product code from SYSTEM.CNF
 *   disc  other discs: the product code in the Saturn or Mega-CD header
 * Discs read only a few sectors (a .chd through libchdr, which decompresses just those). Files
 * over 1 GB aren't hashed. It reads, never writes. */
#include <libchdr/chd.h>
#include <libchdr/cdrom.h>
#include <ctype.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <strings.h>

#define MAX_HASH_SIZE (1024L * 1024 * 1024)

static uint32_t crc_table[256];

static void crc_init(void)
{
	for (uint32_t i = 0; i < 256; i++) {
		uint32_t c = i;
		for (int k = 0; k < 8; k++) c = c & 1 ? 0xEDB88320u ^ (c >> 1) : c >> 1;
		crc_table[i] = c;
	}
}

static uint32_t crc_update(uint32_t crc, const unsigned char *p, size_t n)
{
	crc = ~crc;
	while (n--) crc = crc_table[(crc ^ *p++) & 0xff] ^ (crc >> 8);
	return ~crc;
}

enum { N64_NONE, N64_Z64, N64_V64, N64_N64 };

static long file_size(const char *path)
{
	FILE *f = fopen(path, "rb");
	if (!f) return -1;
	fseek(f, 0, SEEK_END);
	long s = ftell(f);
	fclose(f);
	return s;
}

static int crc_file(int snes, const char *path, char out[9], long *size)
{
	out[0] = 0;
	FILE *f = fopen(path, "rb");
	if (!f) return -1;
	fseek(f, 0, SEEK_END);
	*size = ftell(f);
	fseek(f, 0, SEEK_SET);
	if (*size <= 0 || *size > MAX_HASH_SIZE) { fclose(f); return 0; }
	unsigned char head[128];
	size_t hn = fread(head, 1, sizeof(head), f);
	long skip = 0;
	int order = N64_NONE;
	if (hn >= 16 && (!memcmp(head, "NES\x1a", 4) || !memcmp(head, "FDS\x1a", 4))) skip = 16;
	else if (snes && *size % 1024 == 512) skip = 512;
	else if (hn >= 10 && !memcmp(head + 1, "ATARI7800", 9)) skip = 128;
	else if (hn >= 4 && !memcmp(head, "LYNX", 4)) skip = 64;
	else if (hn >= 4) {
		uint32_t m = (uint32_t)head[0] << 24 | head[1] << 16 | head[2] << 8 | head[3];
		order = m == 0x80371240u ? N64_Z64 : m == 0x37804012u ? N64_V64 : m == 0x40123780u ? N64_N64 : N64_NONE;
	}
	fseek(f, skip, SEEK_SET);
	static unsigned char buf[256 * 1024];
	uint32_t crc = 0;
	size_t n;
	while ((n = fread(buf, 1, sizeof(buf), f)) > 0) {
		if (order == N64_V64) {
			for (size_t i = 0; i + 1 < n; i += 2) { unsigned char t = buf[i]; buf[i] = buf[i + 1]; buf[i + 1] = t; }
		} else if (order == N64_N64) {
			for (size_t i = 0; i + 3 < n; i += 4) {
				unsigned char a = buf[i], b = buf[i + 1];
				buf[i] = buf[i + 3]; buf[i + 1] = buf[i + 2]; buf[i + 2] = b; buf[i + 3] = a;
			}
		}
		crc = crc_update(crc, buf, n);
	}
	int err = ferror(f);
	fclose(f);
	if (err) return -1;
	snprintf(out, 9, "%08x", crc);
	return 0;
}

/* ---------- discs ---------- */

typedef struct {
	FILE *f;
	int sector, offset;
	chd_file *chd;
	uint32_t hunkbytes, frames_per_hunk, pregap;
	long cached_hunk;
	unsigned char *hunk;
} disc_t;

static const unsigned char SYNC[12] = { 0, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0 };

static int disc_read(disc_t *d, long lba, unsigned char out[2048])
{
	if (d->chd) {
		long frame = lba + (long)d->pregap;
		long hunk = frame / (long)d->frames_per_hunk;
		if (hunk != d->cached_hunk) {
			if (chd_read(d->chd, (uint32_t)hunk, d->hunk) != CHDERR_NONE) return -1;
			d->cached_hunk = hunk;
		}
		const unsigned char *f = d->hunk + (frame % (long)d->frames_per_hunk) * CD_FRAME_SIZE;
		int off = !memcmp(f, SYNC, 12) ? (f[15] == 2 ? 24 : 16) : 0;
		memcpy(out, f + off, 2048);
		return 0;
	}
	if (fseek(d->f, lba * d->sector + d->offset, SEEK_SET)) return -1;
	return fread(out, 1, 2048, d->f) == 2048 ? 0 : -1;
}

static int chd_disc_open(const char *path, disc_t *d)
{
	memset(d, 0, sizeof(*d));
	if (chd_open(path, CHD_OPEN_READ, NULL, &d->chd) != CHDERR_NONE) { d->chd = NULL; return -1; }
	const chd_header *h = chd_get_header(d->chd);
	d->hunkbytes = h->hunkbytes;
	d->frames_per_hunk = h->hunkbytes / CD_FRAME_SIZE;
	if (!d->frames_per_hunk) { chd_close(d->chd); d->chd = NULL; return -1; }
	char meta[256];
	uint32_t len = 0;
	if (chd_get_metadata(d->chd, CDROM_TRACK_METADATA2_TAG, 0, meta, sizeof(meta) - 1, &len, NULL, NULL) == CHDERR_NONE) {
		meta[len < sizeof(meta) ? len : sizeof(meta) - 1] = 0;
		int pregap = 0;
		char pgtype[32] = "";
		const char *p = strstr(meta, "PREGAP:");
		if (p) pregap = atoi(p + 7);
		p = strstr(meta, "PGTYPE:");
		if (p) sscanf(p + 7, "%31s", pgtype);
		if (pgtype[0] == 'V') d->pregap = (uint32_t)pregap;
	}
	d->hunk = malloc(d->hunkbytes);
	d->cached_hunk = -1;
	if (!d->hunk) { chd_close(d->chd); d->chd = NULL; return -1; }
	return 0;
}

static void disc_close(disc_t *d)
{
	if (d->chd) { chd_close(d->chd); free(d->hunk); d->chd = NULL; }
	if (d->f) { fclose(d->f); d->f = NULL; }
}

/* the first data track of a .cue (or a .bin/.iso itself); track gets its file */
static int disc_open(const char *path, disc_t *d, char *track, size_t tn)
{
	snprintf(track, tn, "%s", path);
	const char *dot = strrchr(path, '.');
	if (dot && !strcasecmp(dot, ".cue")) {
		FILE *c = fopen(path, "r");
		if (!c) return -1;
		char line[1200], name[1000] = "";
		while (fgets(line, sizeof(line), c)) {
			char *p = line;
			while (isspace((unsigned char)*p)) p++;
			if (!strncasecmp(p, "FILE", 4) && !name[0]) {
				char *q = strchr(p, '"'), *e = q ? strchr(q + 1, '"') : NULL;
				if (q && e) { *e = 0; snprintf(name, sizeof(name), "%s", q + 1); }
				else if (sscanf(p + 4, " %999s", name) != 1) name[0] = 0;
			}
		}
		fclose(c);
		if (!name[0]) return -1;
		const char *slash = strrchr(path, '/');
		if (name[0] == '/' || !slash) snprintf(track, tn, "%s", name);
		else snprintf(track, tn, "%.*s/%s", (int)(slash - path), path, name);
	} else if (dot && !strcasecmp(dot, ".chd")) {
		return chd_disc_open(path, d);
	}
	memset(d, 0, sizeof(*d));
	d->f = fopen(track, "rb");
	if (!d->f) return -1;
	unsigned char h[16];
	if (fread(h, 1, 16, d->f) != 16) { fclose(d->f); d->f = NULL; return -1; }
	if (!memcmp(h, SYNC, 12)) { d->sector = 2352; d->offset = h[15] == 2 ? 24 : 16; }
	else { d->sector = 2048; d->offset = 0; }
	return 0;
}

static void trim_copy(char *dst, int n, const unsigned char *src, int len)
{
	while (len > 0 && (src[len - 1] == ' ' || src[len - 1] == 0)) len--;
	int s = 0;
	while (s < len && src[s] == ' ') s++;
	int j = 0;
	for (int i = s; i < len && j + 1 < n; i++) dst[j++] = isprint(src[i]) && src[i] != '\t' ? (char)src[i] : '?';
	dst[j] = 0;
}

/* SLUS_005.94 -> SLUS-00594 */
static void psx_serial(const char *raw, char *out, int n)
{
	char t[32];
	int j = 0;
	for (const char *p = raw; *p && j < 31; p++)
		if (isalnum((unsigned char)*p)) t[j++] = (char)toupper((unsigned char)*p);
	t[j] = 0;
	int a = 0;
	while (isalpha((unsigned char)t[a])) a++;
	if (a == 5 && isdigit((unsigned char)t[a])) { memmove(t + 4, t + 5, strlen(t + 5) + 1); a = 4; }
	if (a >= 3 && a <= 4 && t[a]) snprintf(out, (size_t)n, "%.*s-%s", a, t, t + a);
	else snprintf(out, (size_t)n, "%s", t);
}

static int psx_boot(disc_t *d, char *serial, int n)
{
	unsigned char s[2048];
	if (disc_read(d, 16, s) || s[0] != 1 || memcmp(s + 1, "CD001", 5)) return -1;
	const unsigned char *root = s + 156;
	long lba = root[2] | root[3] << 8 | root[4] << 16 | (long)root[5] << 24;
	long len = root[10] | root[11] << 8 | root[12] << 16 | (long)root[13] << 24;
	for (long sec = 0; sec * 2048 < len && sec < 16; sec++) {
		unsigned char dir[2048];
		if (disc_read(d, lba + sec, dir)) return -1;
		for (int i = 0; i < 2048;) {
			int rl = dir[i];
			if (!rl || i + rl > 2048) break;
			int nl = dir[i + 32];
			if (nl >= 10 && !strncasecmp((const char *)dir + i + 33, "SYSTEM.CNF", 10)) {
				long flba = dir[i + 2] | dir[i + 3] << 8 | dir[i + 4] << 16 | (long)dir[i + 5] << 24;
				unsigned char cnf[2049];
				if (disc_read(d, flba, cnf)) return -1;
				cnf[2048] = 0;
				char *b = strstr((char *)cnf, "BOOT");
				if (!b) return -1;
				char *st = strchr(b, '=');
				if (!st) return -1;
				char *name = st + 1, *e;
				for (char *p = name; *p && *p != '\n' && *p != '\r' && *p != ';'; p++)
					if (*p == '\\' || *p == ':' || *p == '/') name = p + 1;
				for (e = name; *e && *e != ';' && *e != '\r' && *e != '\n' && *e != ' '; e++) ;
				*e = 0;
				if (!*name) return -1;
				psx_serial(name, serial, n);
				return 0;
			}
			i += rl;
		}
	}
	return -1;
}

/* product code and the size of the data track (what the Redump lists give per track) */
static void disc_ident(int psx, const char *path, char *serial, int n, long *size)
{
	char track[1200];
	disc_t d;
	serial[0] = 0;
	*size = -1;
	if (disc_open(path, &d, track, sizeof(track))) return;
	unsigned char s[2048];
	if (psx) {
		if (psx_boot(&d, serial, n)) serial[0] = 0;
	} else if (!disc_read(&d, 0, s)) {
		if (!memcmp(s, "SEGA SEGASATURN", 15)) trim_copy(serial, n, s + 0x20, 10);
		else if (!memcmp(s, "SEGADISCSYSTEM", 14)) trim_copy(serial, n, s + 0x180, 14);
	}
	if (!d.chd) *size = file_size(track);
	disc_close(&d);
}

int main(void)
{
	crc_init();
	char line[4200];
	while (fgets(line, sizeof(line), stdin)) {
		line[strcspn(line, "\r\n")] = 0;
		char *tab = strchr(line, '\t');
		if (!tab) continue;
		*tab = 0;
		const char *kind = line, *path = tab + 1;
		char crc[9] = "", serial[32] = "";
		long size = -1;
		if (!strcmp(kind, "psx") || !strcmp(kind, "disc")) disc_ident(!strcmp(kind, "psx"), path, serial, sizeof(serial), &size);
		else if (crc_file(!strcmp(kind, "snes"), path, crc, &size)) crc[0] = 0;
		printf("%s\t%s\t%s\t%ld\n", path, crc, serial, size);
		fflush(stdout);
	}
	return 0;
}
