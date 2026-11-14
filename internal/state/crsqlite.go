package state

/*
#cgo linux,amd64 LDFLAGS: -L${SRCDIR}/../../.build/crsqlite/lib/linux_amd64 -lcrsqlite -lm
#cgo linux,arm64 LDFLAGS: -L${SRCDIR}/../../.build/crsqlite/lib/linux_arm64 -lcrsqlite -lm
#cgo darwin,amd64 LDFLAGS: -L${SRCDIR}/../../.build/crsqlite/lib/darwin_amd64 -lcrsqlite
#cgo darwin,arm64 LDFLAGS: -L${SRCDIR}/../../.build/crsqlite/lib/darwin_arm64 -lcrsqlite
#cgo windows,amd64 LDFLAGS: -L${SRCDIR}/../../.build/crsqlite/lib/windows_amd64 -lcrsqlite
extern int sqlite3_auto_extension(void (*entry)(void));
extern int sqlite3_crsqlite_init(void *, char **, const void *);
static int register_crsqlite(void) {
 return sqlite3_auto_extension((void (*)(void))sqlite3_crsqlite_init);
}
*/
import "C"

func init() {
	if C.register_crsqlite() != 0 {
		panic("register statically linked cr-sqlite")
	}
}
