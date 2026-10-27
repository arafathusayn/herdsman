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

/*
#include <errno.h>
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
		close(fd);
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
