#include <sys/socket.h>
#include "adapted/syscall.h" // TINYGO

int connect(int fd, const struct sockaddr *addr, socklen_t len)
{
	return socketcall_cp(connect, fd, addr, len, 0, 0, 0);
}
