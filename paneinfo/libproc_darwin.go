// libproc_darwin.go
// Author: Kennt Kim
// Created: 2026-10-23 · Last updated: 2026-10-23
//
// Purpose: what macOS tells only through libproc's C functions (system_darwin.go): a process's
// working directory, the paths of its open files and its listening TCP sockets; and the process
// table read into a buffer kept between reads.
//
// Notes:
// - Built with cgo (the Mac keeper is built on a Mac with Xcode's clang); without cgo
//   libproc_nocgo_darwin.go answers "not known" instead.
// - The directory is proc_pidinfo(PROC_PIDVNODEPATHINFO)'s pvi_cdir; the descriptors are
//   proc_pidinfo(PROC_PIDLISTFDS) (proc_fdinfo: the descriptor and its PROX_FDTYPE_*); a file's path
//   is proc_pidfdinfo(PROC_PIDFDVNODEPATHINFO)'s pvip.vip_path, a socket's state
//   proc_pidfdinfo(PROC_PIDFDSOCKETINFO)'s socket_fdinfo: soi_kind SOCKINFO_TCP, tcpsi_state
//   TSI_S_LISTEN, and the local address and port of tcpsi_ini (insi_lport in network byte order,
//   as lsof reads it). The C helpers read the unions Go cannot name.
// - Each call answers with the size it filled; anything short of the structure (the process ended,
//   another user's process) is "not known".
// - The process table: x/sys's SysctlKinfoProcSlice sizes its buffer by asking the kernel first,
//   and the kernel answers for every process whatever the filter — 850 KB a read for a terminal of
//   a few processes (BenchmarkRead, 2026-10-23). Here sysctl fills a buffer the Reader keeps,
//   grown when the kernel says ENOMEM. It is a []byte, so the collector never takes the kernel's
//   addresses in kinfo_proc's pointer fields for pointers; at 64 entries and more it is a large
//   allocation, page-aligned, so it can be read as kinfo_procs.
// - An IPv6 socket that also takes IPv4 (Go's and most servers' `:port`) has INI_IPV6 with `::`:
//   its IPv6 address is taken first, so it is Everywhere.

//go:build darwin && cgo && !ios

package paneinfo

/*
#include <libproc.h>
#include <sys/proc_info.h>
#include <sys/sysctl.h>
#include <arpa/inet.h>
#include <string.h>

// sd_listener is a TCP socket in LISTEN: its port, and its local address (IPv4 in the first 4
// bytes when v4 is set).
typedef struct {
	int port;
	int v4;
	unsigned char addr[16];
} sd_listener;

// sd_tcp_listener reads descriptor fd of pid: 1 with *out filled when it is a listening TCP
// socket, else 0.
static int sd_tcp_listener(int pid, int fd, sd_listener *out) {
	struct socket_fdinfo si;
	if (proc_pidfdinfo(pid, fd, PROC_PIDFDSOCKETINFO, &si, sizeof si) != (int)sizeof si) {
		return 0;
	}
	if (si.psi.soi_kind != SOCKINFO_TCP) {
		return 0;
	}
	const struct tcp_sockinfo *tcp = &si.psi.soi_proto.pri_tcp;
	if (tcp->tcpsi_state != TSI_S_LISTEN) {
		return 0;
	}
	const struct in_sockinfo *in = &tcp->tcpsi_ini;
	out->port = ntohs((uint16_t)in->insi_lport);
	if (in->insi_vflag & INI_IPV6) {
		out->v4 = 0;
		memcpy(out->addr, &in->insi_laddr.ina_6, 16);
	} else if (in->insi_vflag & INI_IPV4) {
		out->v4 = 1;
		memcpy(out->addr, &in->insi_laddr.ina_46.i46a_addr4, 4);
	} else {
		return 0;
	}
	return 1;
}

// sd_fd_path writes the path of descriptor fd of pid (a file) into out: 1 when it could.
static int sd_fd_path(int pid, int fd, char *out, int size) {
	struct vnode_fdinfowithpath vi;
	if (proc_pidfdinfo(pid, fd, PROC_PIDFDVNODEPATHINFO, &vi, sizeof vi) != (int)sizeof vi) {
		return 0;
	}
	strlcpy(out, vi.pvip.vip_path, size);
	return 1;
}

// sd_cwd writes pid's working directory into out: 1 when it could.
static int sd_cwd(int pid, char *out, int size) {
	struct proc_vnodepathinfo vpi;
	if (proc_pidinfo(pid, PROC_PIDVNODEPATHINFO, 0, &vpi, sizeof vpi) != (int)sizeof vpi) {
		return 0;
	}
	strlcpy(out, vpi.pvi_cdir.vip_path, size);
	return 1;
}
*/
import "C"

import (
	"errors"
	"net"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// readsProcessFiles says whether this build reads a process's folder, open files and sockets.
const readsProcessFiles = true

// kinfo_proc as C has it and as x/sys/unix has it are the same size.
var (
	_ [C.sizeof_struct_kinfo_proc - unix.SizeofKinfoProc]byte
	_ [unix.SizeofKinfoProc - C.sizeof_struct_kinfo_proc]byte
)

// maxProcBuf bounds the process table's buffer, in bytes (kern.maxproc is a few thousand).
const maxProcBuf = 64 << 20

// processes is the processes whose controlling terminal is tdev, or every process for nodev.
func (d *darwin) processes(tdev int32) ([]unix.KinfoProc, error) {
	mib := []C.int{C.CTL_KERN, C.KERN_PROC, C.KERN_PROC_ALL}
	if tdev != nodev {
		mib = []C.int{C.CTL_KERN, C.KERN_PROC, C.KERN_PROC_TTY, C.int(tdev)}
	}
	if len(d.procbuf) == 0 {
		d.procbuf = make([]byte, 64*unix.SizeofKinfoProc)
	}
	for {
		size := C.size_t(len(d.procbuf))
		r, err := C.sysctl(&mib[0], C.u_int(len(mib)), unsafe.Pointer(&d.procbuf[0]), &size, nil, 0)
		if r != 0 {
			if errors.Is(err, syscall.ENOMEM) && len(d.procbuf) < maxProcBuf {
				d.procbuf = make([]byte, 2*len(d.procbuf))
				continue
			}
			return nil, err
		}
		n := int(size) / unix.SizeofKinfoProc
		if n == 0 {
			return nil, nil
		}
		return unsafe.Slice((*unix.KinfoProc)(unsafe.Pointer(&d.procbuf[0])), n), nil
	}
}

// libprocDirectory is pid's working directory, "" when not known.
func libprocDirectory(pid int) string {
	var buf [C.MAXPATHLEN]C.char
	if C.sd_cwd(C.int(pid), &buf[0], C.int(len(buf))) == 0 {
		return ""
	}
	return C.GoString(&buf[0])
}

// descriptors is pid's open descriptors.
func descriptors(pid int) []C.struct_proc_fdinfo {
	const size = C.sizeof_struct_proc_fdinfo
	n := C.proc_pidinfo(C.int(pid), C.PROC_PIDLISTFDS, 0, nil, 0)
	if n <= 0 {
		return nil
	}
	// n is the room the descriptors take now; a few more for those opened meanwhile.
	buf := make([]C.struct_proc_fdinfo, int(n)/size+8)
	n = C.proc_pidinfo(C.int(pid), C.PROC_PIDLISTFDS, 0, unsafe.Pointer(&buf[0]), C.int(len(buf)*size))
	if n <= 0 {
		return nil
	}
	return buf[:int(n)/size]
}

// libprocOpenFiles is the paths of the files pid holds open.
func libprocOpenFiles(pid int) []string {
	var out []string
	var buf [C.MAXPATHLEN]C.char
	for _, fd := range descriptors(pid) {
		if fd.proc_fdtype != C.PROX_FDTYPE_VNODE {
			continue
		}
		if C.sd_fd_path(C.int(pid), fd.proc_fd, &buf[0], C.int(len(buf))) != 0 && buf[0] != 0 {
			out = append(out, C.GoString(&buf[0]))
		}
	}
	return out
}

// libprocListeners calls found with each TCP socket pid listens on.
func libprocListeners(pid int, found func(listener)) {
	for _, fd := range descriptors(pid) {
		if fd.proc_fdtype != C.PROX_FDTYPE_SOCKET {
			continue
		}
		var l C.sd_listener
		if C.sd_tcp_listener(C.int(pid), fd.proc_fd, &l) == 0 {
			continue
		}
		addr := C.GoBytes(unsafe.Pointer(&l.addr[0]), 16)
		if l.v4 != 0 {
			addr = addr[:4]
		}
		found(listener{ip: net.IP(addr), port: uint16(l.port)})
	}
}
