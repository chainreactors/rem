#include <sys/socket.h>
#include "adapted/syscall.h" // TINYGO

int listen(int fd, int backlog)
{
	return socketcall(listen, fd, backlog, 0, 0, 0, 0);
}
