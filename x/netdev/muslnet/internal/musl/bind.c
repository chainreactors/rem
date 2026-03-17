#include <sys/socket.h>
#include "adapted/syscall.h" // TINYGO

int bind(int fd, const struct sockaddr *addr, socklen_t len)
{
	return socketcall(bind, fd, addr, len, 0, 0, 0);
}
