//go:build darwin || linux

package store

// SQLite's unix VFS opens every file it uses (the database, -wal, -shm,
// the folder it syncs, /dev/urandom) through its system-call table. The
// store replaces that table's "open" with checkedOpen, so the checks of
// Open hold for the descriptor SQLite really uses, not only for the name
// Open looked at before (S1-1 F1): it never blocks on a FIFO
// (O_NONBLOCK), never follows a link (O_NOFOLLOW), and refuses anything
// but a regular file of mode 600 or a folder of mode 700 owned by this
// user. The table is process-wide; the store is the only SQLite user.
//
// A refused descriptor is closed only when no other descriptor of the
// process holds its inode: closing any descriptor of an inode drops every
// POSIX lock the process holds on it, so a refused open of a file a
// connection locks (the database, its -wal or -shm) would drop that
// connection's locks (S27N1-1 F2). Such a descriptor is parked instead,
// and closeParked closes it once nothing else holds its inode.

/*
#include <dirent.h>
#include <errno.h>
#include <pthread.h>
#include <stdlib.h>
#include <fcntl.h>
#include <string.h>
#include <sys/stat.h>
#include <unistd.h>

// The stable sqlite3_vfs layout (iVersion 3), declared here so this file
// needs no SQLite header; the driver's SQLite provides sqlite3_vfs_find.
typedef struct sqlite3_vfs sqlite3_vfs;
typedef void (*sqlite3_syscall_ptr)(void);
struct sqlite3_vfs {
	int iVersion;
	int szOsFile;
	int mxPathname;
	sqlite3_vfs *pNext;
	const char *zName;
	void *pAppData;
	void *xOpen, *xDelete, *xAccess, *xFullPathname, *xDlOpen, *xDlError, *xDlSym, *xDlClose;
	void *xRandomness, *xSleep, *xCurrentTime, *xGetLastError, *xCurrentTimeInt64;
	int (*xSetSystemCall)(sqlite3_vfs *, const char *, sqlite3_syscall_ptr);
	sqlite3_syscall_ptr (*xGetSystemCall)(sqlite3_vfs *, const char *);
	const char *(*xNextSystemCall)(sqlite3_vfs *, const char *);
};
extern sqlite3_vfs *sqlite3_vfs_find(const char *) __attribute__((weak));

static pthread_mutex_t herdsman_park_mu = PTHREAD_MUTEX_INITIALIZER;
static int *herdsman_parked;
static int herdsman_nparked, herdsman_capparked;

static int herdsman_is_parked(int fd) {
	for (int i = 0; i < herdsman_nparked; i++) {
		if (herdsman_parked[i] == fd) {
			return 1;
		}
	}
	return 0;
}

// herdsman_shared reports whether a descriptor of this process other than
// fd and the parked ones holds fd's inode; 1 when it cannot tell. The
// caller holds herdsman_park_mu.
static int herdsman_shared(int fd) {
	struct stat st;
	if (fstat(fd, &st) != 0) {
		return 1;
	}
#ifdef __linux__
	DIR *d = opendir("/proc/self/fd");
#else
	DIR *d = opendir("/dev/fd");
#endif
	if (!d) {
		return 1;
	}
	int own = dirfd(d), found = 0;
	struct dirent *e;
	while (!found && (e = readdir(d)) != NULL) {
		char *end;
		long n = strtol(e->d_name, &end, 10);
		if (end == e->d_name || *end != 0 || n == fd || n == own || herdsman_is_parked((int)n)) {
			continue;
		}
		struct stat other;
		found = fstat((int)n, &other) == 0 && other.st_dev == st.st_dev && other.st_ino == st.st_ino;
	}
	closedir(d);
	return found;
}

// herdsman_drop closes a refused descriptor, or parks it while another
// descriptor of the process holds its inode.
static void herdsman_drop(int fd) {
	pthread_mutex_lock(&herdsman_park_mu);
	if (herdsman_shared(fd)) {
		if (herdsman_nparked == herdsman_capparked) {
			int cap = herdsman_capparked ? 2 * herdsman_capparked : 8;
			int *grown = realloc(herdsman_parked, cap * sizeof(int));
			if (grown) {
				herdsman_parked = grown;
				herdsman_capparked = cap;
			}
		}
		// Out of memory: the descriptor leaks rather than drop locks.
		if (herdsman_nparked < herdsman_capparked) {
			herdsman_parked[herdsman_nparked++] = fd;
		}
	} else {
		close(fd);
	}
	pthread_mutex_unlock(&herdsman_park_mu);
}

// herdsman_close_parked closes the parked descriptors whose inode nothing
// else holds any more; it returns how many stay parked.
static int herdsman_close_parked(void) {
	pthread_mutex_lock(&herdsman_park_mu);
	int kept = 0;
	for (int i = 0; i < herdsman_nparked; i++) {
		int fd = herdsman_parked[i];
		if (herdsman_shared(fd)) {
			herdsman_parked[kept++] = fd;
		} else {
			close(fd);
		}
	}
	herdsman_nparked = kept;
	pthread_mutex_unlock(&herdsman_park_mu);
	return kept;
}

static int herdsman_checked_open(const char *z, int flags, int mode) {
	int fd = open(z, flags | O_NONBLOCK | O_NOFOLLOW | O_CLOEXEC, mode);
	if (fd < 0) {
		return fd;
	}
	struct stat st;
	int ok = fstat(fd, &st) == 0;
	if (ok && S_ISCHR(st.st_mode)) {
		ok = strcmp(z, "/dev/urandom") == 0;
	} else if (ok) {
		ok = st.st_uid == geteuid() &&
			((S_ISREG(st.st_mode) && (st.st_mode & 0777) == 0600) ||
			 (S_ISDIR(st.st_mode) && (st.st_mode & 0777) == 0700));
	}
	if (ok) {
		int fl = fcntl(fd, F_GETFL);
		ok = fl >= 0 && fcntl(fd, F_SETFL, fl & ~O_NONBLOCK) == 0;
	}
	if (!ok) {
		herdsman_drop(fd);
		errno = EACCES;
		return -1;
	}
	return fd;
}

// herdsman_install puts the checked open into the unix VFS: 0 on success.
static int herdsman_install(void) {
	if (!sqlite3_vfs_find) {
		return -1;
	}
	sqlite3_vfs *v = sqlite3_vfs_find(0);
	if (!v || v->iVersion < 3 || !v->xSetSystemCall) {
		return -2;
	}
	return v->xSetSystemCall(v, "open", (sqlite3_syscall_ptr)herdsman_checked_open);
}

// herdsman_installed reports whether the unix VFS uses the checked open.
static int herdsman_installed(void) {
	if (!sqlite3_vfs_find) {
		return 0;
	}
	sqlite3_vfs *v = sqlite3_vfs_find(0);
	return v && v->iVersion >= 3 && v->xGetSystemCall &&
		v->xGetSystemCall(v, "open") == (sqlite3_syscall_ptr)herdsman_checked_open;
}
*/
import "C"

import (
	"fmt"
	"sync"
	"unsafe"
)

var (
	vfsOnce sync.Once
	vfsErr  error
)

// installCheckedOpen runs once, before the first SQLite open.
func installCheckedOpen() error {
	vfsOnce.Do(func() {
		if rc := C.herdsman_install(); rc != 0 {
			vfsErr = fmt.Errorf("store: cannot install the checked open in SQLite's VFS (%d)", int(rc))
		}
	})
	return vfsErr
}

// closeParked closes the refused descriptors parked by the checked open
// whose inode no other descriptor of the process holds any more; it
// reports how many stay parked.
func closeParked() int { return int(C.herdsman_close_parked()) }

// checkedOpenInstalled reports whether SQLite opens files through it.
func checkedOpenInstalled() bool { return C.herdsman_installed() == 1 }

// tryCheckedOpen opens p read-only through the checked open and closes
// it again; it reports whether the checked open accepted p.
func tryCheckedOpen(p string) bool {
	cs := C.CString(p)
	defer C.free(unsafe.Pointer(cs))
	fd := C.herdsman_checked_open(cs, C.O_RDONLY, 0)
	if fd < 0 {
		return false
	}
	C.close(fd)
	return true
}
