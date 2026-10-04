/*
 * dbtool exercises a system dbopen(3), e.g. macOS libc, to cross-check
 * this package.
 *
 *	cc -o dbtool dbtool.c
 *	dbtool mk btree|hash|recno file n	write n pairs, delete every 5th
 *	dbtool dump btree|hash|recno file	print key/data sizes and FNV-1a
 *	dbtool check btree|hash file n		get every pair written by mk
 *
 * LORDER and BSIZE environment variables set lorder and psize/bsize on mk.
 * MKDIR=dir go test -run TestMk ./dbtest writes the same files
 * with Go for dbtool to check.
 */
#include <db.h>
#include <fcntl.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

static unsigned fnv(const unsigned char *p, size_t n) {
	unsigned h = 2166136261u;
	while (n--) { h ^= *p++; h *= 16777619u; }
	return h;
}

static size_t gen(int i, char **k, size_t *kl, char **d) {
	static char kb[8192], db[16384];
	size_t dl = (i * 37) % 300, j;
	if (i % 97 == 0) dl = 5000 + i % 1000;
	snprintf(kb, sizeof(kb), "key%06d", i);
	*kl = strlen(kb);
	if (i % 151 == 0) {
		for (j = *kl; j < 3000; j++) kb[j] = 'A' + (i + j) % 26;
		*kl = 3000;
	}
	for (j = 0; j < dl; j++) db[j] = 'a' + (i + j) % 26;
	*k = kb; *d = db;
	return dl;
}

int main(int argc, char **argv) {
	DBTYPE type = !strcmp(argv[2], "hash") ? DB_HASH : !strcmp(argv[2], "recno") ? DB_RECNO : DB_BTREE;
	int mk = !strcmp(argv[1], "mk"), n = argc > 4 ? atoi(argv[4]) : 0, i;
	BTREEINFO bi; HASHINFO hi; void *info = NULL;
	memset(&bi, 0, sizeof(bi)); memset(&hi, 0, sizeof(hi));
	if (mk && getenv("LORDER")) { bi.lorder = hi.lorder = atoi(getenv("LORDER")); info = type == DB_HASH ? (void *)&hi : (void *)&bi; }
	if (mk && getenv("BSIZE")) { hi.bsize = bi.psize = atoi(getenv("BSIZE")); info = type == DB_HASH ? (void *)&hi : (void *)&bi; }
	DB *db = dbopen(argv[3], mk ? O_CREAT|O_RDWR|O_TRUNC : O_RDONLY, 0644, type, info);
	DBT key, data;
	char *k, *d; size_t kl;
	unsigned rn;
	if (!db) { perror("dbopen"); return 1; }
	if (mk) {
		for (i = 0; i < n; i++) {
			data.size = gen(i, &k, &kl, &d); data.data = d;
			if (type == DB_RECNO) { rn = i + 1; key.data = &rn; key.size = 4; data.size = data.size % 200; }
			else { key.data = k; key.size = kl; }
			if (db->put(db, &key, &data, 0)) { perror("put"); return 1; }
		}
		/* delete every 5th */
		for (i = 0; type != DB_RECNO && i < n; i += 5) {
			gen(i, &k, &kl, &d); key.data = k; key.size = kl;
			if (db->del(db, &key, 0)) { fprintf(stderr, "del %d failed\n", i); }
		}
	} else if (!strcmp(argv[1], "check")) {
		for (i = 0; i < n; i++) {
			size_t dl = gen(i, &k, &kl, &d);
			key.data = k; key.size = kl;
			int r = db->get(db, &key, &data, 0);
			if (i % 5 == 0) { if (r != 1) printf("%d: deleted but r=%d\n", i, r); continue; }
			if (r != 0) printf("%d: missing r=%d\n", i, r);
			else if (data.size != dl || memcmp(data.data, d, dl)) printf("%d: bad data\n", i);
		}
	} else {
		int fl = R_FIRST;
		for (; db->seq(db, &key, &data, fl) == 0; fl = R_NEXT) {
			if (type == DB_RECNO) printf("%u", *(unsigned *)key.data);
			else printf("%zu:%08x", key.size, fnv(key.data, key.size));
			printf(" %zu:%08x\n", data.size, fnv(data.data, data.size));
		}
	}
	return db->close(db) ? 1 : 0;
}
