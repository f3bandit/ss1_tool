/* ss1trimfs: TRIM for an exFAT drive whose Linux driver can't (MiSTer's 5.15 kernel uses the old Samsung exFAT
 * driver, which has no FITRIM: fstrim answers "not supported" whatever the drive). It does what fstrim would:
 * reads exFAT's own allocation bitmap and tells the drive (BLKDISCARD on the partition) which clusters are free.
 * Files are never touched: only clusters the bitmap marks free are discarded.
 *
 * Safety: the file system is remounted read-only first, so nothing can be written (or allocated) while the
 * bitmap is read and the free space discarded; a file open for writing makes that remount fail, and then
 * nothing is done ("busy"). The bitmap must agree with what the file system reports as free (statfs), within
 * a cluster per 1000, or nothing is discarded. Then it's remounted read-write.
 *
 *   ss1trimfs <mount point>           trim the free space; prints key=value (trimmed=, free=, clusters=...)
 *   ss1trimfs -n <mount point>        only check (read-only remount, the bitmap read and compared, no discard)
 *   ss1trimfs -i <image or device>    not mounted (tests): the image's free clusters (a file: holes punched)
 *
 * Copyright (C) 2026 f3bandit. PolyForm Noncommercial 1.0.0 (see LICENSE.md in the repo). */
#define _GNU_SOURCE
#include <errno.h>
#include <fcntl.h>
#include <linux/falloc.h>
#include <linux/fs.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/ioctl.h>
#include <sys/wait.h>
#include <sys/stat.h>
#include <sys/statfs.h>
#include <unistd.h>

static uint32_t le32(const uint8_t *p) { return p[0] | p[1] << 8 | p[2] << 16 | (uint32_t)p[3] << 24; }
static uint64_t le64(const uint8_t *p) { return le32(p) | (uint64_t)le32(p + 4) << 32; }

typedef struct {
	int fd, is_file;
	uint64_t size;                 /* bytes */
	uint32_t fat_off, fat_len, heap_off, nclusters, root;   /* sectors, sectors, sectors, count, cluster */
	int bps_shift, spc_shift;
	uint64_t csize;                /* cluster bytes */
} vol_t;

static int rd(const vol_t *v, uint64_t off, void *b, size_t n)
{
	size_t got = 0;
	while (got < n) {
		ssize_t r = pread(v->fd, (char *)b + got, n - got, (off_t)(off + got));
		if (r <= 0) return -1;
		got += (size_t)r;
	}
	return 0;
}

static uint64_t cl_off(const vol_t *v, uint32_t c) { return ((uint64_t)v->heap_off << v->bps_shift) + (uint64_t)(c - 2) * v->csize; }

/* the next cluster in a FAT chain (0xFFFFFFFF: end) */
static uint32_t fat_next(const vol_t *v, uint32_t c)
{
	uint8_t b[4];
	if (rd(v, ((uint64_t)v->fat_off << v->bps_shift) + (uint64_t)c * 4, b, 4)) return 0xFFFFFFFF;
	return le32(b);
}

static int open_vol(vol_t *v, const char *dev, int wr)
{
	memset(v, 0, sizeof(*v));
	v->fd = open(dev, (wr ? O_RDWR : O_RDONLY) | O_CLOEXEC);
	if (v->fd < 0) return -1;
	struct stat st;
	fstat(v->fd, &st);
	v->is_file = S_ISREG(st.st_mode);
	if (v->is_file) v->size = (uint64_t)st.st_size;
	else if (ioctl(v->fd, BLKGETSIZE64, &v->size)) return -1;
	uint8_t b[512];
	if (rd(v, 0, b, 512) || memcmp(b + 3, "EXFAT   ", 8) || b[510] != 0x55 || b[511] != 0xAA) { errno = EINVAL; return -1; }
	v->fat_off = le32(b + 0x50);
	v->fat_len = le32(b + 0x54);
	v->heap_off = le32(b + 0x58);
	v->nclusters = le32(b + 0x5C);
	v->root = le32(b + 0x60);
	v->bps_shift = b[0x6C];
	v->spc_shift = b[0x6D];
	if (v->bps_shift < 9 || v->bps_shift > 12 || v->spc_shift > 25 || !v->nclusters) { errno = EINVAL; return -1; }
	v->csize = (uint64_t)1 << (v->bps_shift + v->spc_shift);
	if (cl_off(v, v->nclusters + 2) > v->size) { errno = EINVAL; return -1; }
	return 0;
}

/* the allocation bitmap's first cluster and length, from the root directory (entry type 0x81, the first one) */
static int find_bitmap(const vol_t *v, uint32_t *first, uint64_t *len)
{
	uint8_t *buf = malloc(v->csize);
	if (!buf) return -1;
	uint32_t c = v->root;
	for (int hops = 0; c >= 2 && c < v->nclusters + 2 && hops < 1024; hops++) {
		if (rd(v, cl_off(v, c), buf, v->csize)) break;
		for (uint64_t o = 0; o + 32 <= v->csize; o += 32) {
			uint8_t t = buf[o];
			if (t == 0) { free(buf); return -1; }                    /* end of the directory */
			if (t == 0x81 && !(buf[o + 1] & 1)) {                    /* the first bitmap (TexFAT's second: bit 0 set) */
				*first = le32(buf + o + 20);
				*len = le64(buf + o + 24);
				free(buf);
				return 0;
			}
		}
		c = fat_next(v, c);
	}
	free(buf);
	return -1;
}

/* the bitmap (malloc'd, nclusters bits), following its FAT chain */
static uint8_t *read_bitmap(const vol_t *v)
{
	uint32_t c;
	uint64_t len;
	if (find_bitmap(v, &c, &len)) return NULL;
	uint64_t need = (v->nclusters + 7) / 8;
	if (len < need) return NULL;
	uint8_t *bm = malloc(need);
	if (!bm) return NULL;
	uint64_t got = 0;
	for (int hops = 0; got < need && c >= 2 && c < v->nclusters + 2 && hops < (1 << 24); hops++) {
		uint64_t n = need - got < v->csize ? need - got : v->csize;
		if (rd(v, cl_off(v, c), bm + got, n)) { free(bm); return NULL; }
		got += n;
		if (got < need) c = fat_next(v, c);
	}
	if (got < need) { free(bm); return NULL; }
	return bm;
}

/* mount -o remount,<how> <mp>: MiSTer's mount keeps the other options as they are (sync, noexec...) */
static int remount(const char *mp, const char *how)
{
	char o[32];
	snprintf(o, sizeof(o), "remount,%s", how);
	pid_t p = fork();
	if (p == 0) {
		int nul = open("/dev/null", O_RDWR);
		if (nul >= 0) { dup2(nul, 1); dup2(nul, 2); }
		execlp("mount", "mount", "-o", o, mp, (char *)NULL);
		_exit(127);
	}
	int st = 1;
	if (p > 0) waitpid(p, &st, 0);
	return p > 0 && WIFEXITED(st) && WEXITSTATUS(st) == 0 ? 0 : -1;
}

static int discard(const vol_t *v, uint64_t off, uint64_t len)
{
	if (v->is_file) return fallocate(v->fd, FALLOC_FL_PUNCH_HOLE | FALLOC_FL_KEEP_SIZE, (off_t)off, (off_t)len);
	uint64_t r[2] = { off, len };
	return ioctl(v->fd, BLKDISCARD, r);
}

/* the partition device mounted at mp, and its mount options */
static int mount_dev(const char *mp, char *dev, size_t dn, char *opts, size_t on, char *fstype, size_t fn)
{
	FILE *f = fopen("/proc/mounts", "r");
	if (!f) return -1;
	char d[300], m[300], t[64], o[600];
	int found = 0;
	while (fscanf(f, "%299s %299s %63s %599s %*s %*s", d, m, t, o) == 4) if (!strcmp(m, mp)) { snprintf(dev, dn, "%s", d); snprintf(opts, on, "%s", o); snprintf(fstype, fn, "%s", t); found = 1; }
	fclose(f);
	return found ? 0 : -1;
}

int main(int argc, char **argv)
{
	int check_only = 0, image = 0, a = 1;
	for (; a < argc && argv[a][0] == '-'; a++) {
		if (!strcmp(argv[a], "-n")) check_only = 1;
		else if (!strcmp(argv[a], "-i")) image = 1;
		else { fprintf(stderr, "usage: ss1trimfs [-n] <mount point> | -i <image>\n"); return 2; }
	}
	if (a != argc - 1) { fprintf(stderr, "usage: ss1trimfs [-n] <mount point> | -i <image>\n"); return 2; }
	const char *target = argv[a];
	char dev[300] = "", opts[600] = "", fstype[64] = "";
	int remounted = 0;
	struct statfs sf;
	uint64_t fs_free = 0;
	if (image) snprintf(dev, sizeof(dev), "%s", target);
	else {
		if (mount_dev(target, dev, sizeof(dev), opts, sizeof(opts), fstype, sizeof(fstype))) { printf("error=%s isn't mounted\n", target); return 1; }
		if (strcmp(fstype, "exfat")) { printf("error=%s is %s, not exFAT (use fstrim)\n", target, fstype); return 1; }
		sync();
		/* nothing can change while the bitmap is read: read-only (refused while a file is open for writing) */
		if (remount(target, "ro")) { printf("error=busy: something is writing to the drive\n"); return 3; }
		remounted = 1;
		if (statfs(target, &sf) == 0) fs_free = (uint64_t)sf.f_bfree * (uint64_t)sf.f_bsize;
	}
	int rc = 0;
	vol_t v = { .fd = -1 };
	if (open_vol(&v, dev, !check_only)) { printf("error=%s isn't an exFAT volume this can read (%s)\n", dev, strerror(errno)); rc = 1; goto out; }
	uint8_t *bm = read_bitmap(&v);
	if (!bm) { printf("error=couldn't read the allocation bitmap\n"); rc = 1; goto out; }
	uint64_t nfree = 0;
	for (uint32_t i = 0; i < v.nclusters; i++) if (!(bm[i >> 3] >> (i & 7) & 1)) nfree++;
	printf("clusters=%u\ncluster_bytes=%llu\nfree_clusters=%llu\nfree=%llu\n", v.nclusters, (unsigned long long)v.csize, (unsigned long long)nfree, (unsigned long long)(nfree * v.csize));
	if (fs_free) {
		uint64_t mine = nfree * v.csize, diff = mine > fs_free ? mine - fs_free : fs_free - mine;
		printf("fs_free=%llu\n", (unsigned long long)fs_free);
		if (diff > (v.nclusters / 1000 + 2) * v.csize) { printf("error=the bitmap doesn't agree with the file system's free space: nothing done\n"); free(bm); rc = 1; goto out; }
	}
	if (check_only) { printf("checked=1\n"); free(bm); goto out; }
	uint64_t trimmed = 0, runs = 0, errs = 0;
	for (uint32_t i = 0; i < v.nclusters;) {
		if (bm[i >> 3] >> (i & 7) & 1) { i++; continue; }
		uint32_t j = i;
		while (j < v.nclusters && !(bm[j >> 3] >> (j & 7) & 1)) j++;
		uint64_t off = cl_off(&v, i + 2), len = (uint64_t)(j - i) * v.csize;
		if (discard(&v, off, len)) { errs++; if (errs == 1) printf("discard_error=%s\n", strerror(errno)); if (errno == EOPNOTSUPP || errno == ENOTTY) break; }
		else { trimmed += len; runs++; }
		i = j;
	}
	free(bm);
	printf("trimmed=%llu\nruns=%llu\nerrors=%llu\n", (unsigned long long)trimmed, (unsigned long long)runs, (unsigned long long)errs);
	if (errs && !trimmed) rc = 1;
out:
	if (v.fd >= 0) close(v.fd);
	if (remounted && remount(target, "rw") && remount(target, "rw")) { printf("error=couldn't make it writable again: restart the SuperStation\n"); rc = 1; }
	return rc;
}
