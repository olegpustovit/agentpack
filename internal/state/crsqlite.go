package state

/*
#cgo linux,amd64 LDFLAGS: -L${SRCDIR}/../../.build/crsqlite/8e32195704ccaea8508f7d378e11dc2dc0bb3d2e/linux_amd64 -lcrsqlite -lm
#cgo linux,arm64 LDFLAGS: -L${SRCDIR}/../../.build/crsqlite/8e32195704ccaea8508f7d378e11dc2dc0bb3d2e/linux_arm64 -lcrsqlite -lm
#cgo darwin,amd64 LDFLAGS: -L${SRCDIR}/../../.build/crsqlite/8e32195704ccaea8508f7d378e11dc2dc0bb3d2e/darwin_amd64 -lcrsqlite
#cgo darwin,arm64 LDFLAGS: -L${SRCDIR}/../../.build/crsqlite/8e32195704ccaea8508f7d378e11dc2dc0bb3d2e/darwin_arm64 -lcrsqlite
#cgo windows,amd64 LDFLAGS: -L${SRCDIR}/../../.build/crsqlite/8e32195704ccaea8508f7d378e11dc2dc0bb3d2e/windows_amd64 -lcrsqlite
#if defined(__linux__) && defined(__x86_64__)
#if !__has_include("../../.build/crsqlite/8e32195704ccaea8508f7d378e11dc2dc0bb3d2e/linux_amd64/libcrsqlite.a")
#error "cr-sqlite static library missing: run make crsqlite"
#endif
#endif
#if defined(__linux__) && defined(__aarch64__)
#if !__has_include("../../.build/crsqlite/8e32195704ccaea8508f7d378e11dc2dc0bb3d2e/linux_arm64/libcrsqlite.a")
#error "cr-sqlite static library missing: run make crsqlite"
#endif
#endif
#if defined(__APPLE__) && defined(__x86_64__)
#if !__has_include("../../.build/crsqlite/8e32195704ccaea8508f7d378e11dc2dc0bb3d2e/darwin_amd64/libcrsqlite.a")
#error "cr-sqlite static library missing: run make crsqlite"
#endif
#endif
#if defined(__APPLE__) && defined(__aarch64__)
#if !__has_include("../../.build/crsqlite/8e32195704ccaea8508f7d378e11dc2dc0bb3d2e/darwin_arm64/libcrsqlite.a")
#error "cr-sqlite static library missing: run make crsqlite"
#endif
#endif
#if defined(_WIN32) && defined(__x86_64__)
#if !__has_include("../../.build/crsqlite/8e32195704ccaea8508f7d378e11dc2dc0bb3d2e/windows_amd64/libcrsqlite.a")
#error "cr-sqlite static library missing: run make crsqlite"
#endif
#endif
extern int sqlite3_auto_extension(void (*entry)(void));
extern int sqlite3_crsqlite_init(void *, char **, const void *);
extern int sqlite3_busy_timeout(void *, int);
static int initialize_crsqlite(void *db, char **error, const void *api) {
 // Auto-extension setup runs before the Go driver's DSN pragmas.
 int rc = sqlite3_busy_timeout(db, 5000);
 if (rc != 0) return rc;
 return sqlite3_crsqlite_init(db, error, api);
}
static int register_crsqlite(void) {
 return sqlite3_auto_extension((void (*)(void))initialize_crsqlite);
}
*/
import "C"

func init() {
	if C.register_crsqlite() != 0 {
		panic("register statically linked cr-sqlite")
	}
}
