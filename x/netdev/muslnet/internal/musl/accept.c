#include <sys/socket.h>
#include "adapted/syscall.h" // TINYGO

int accept(int fd, struct sockaddr *restrict addr, socklen_t *restrict len)
{
	return socketcall_cp(accept, fd, addr, len, 0, 0, 0);
}
