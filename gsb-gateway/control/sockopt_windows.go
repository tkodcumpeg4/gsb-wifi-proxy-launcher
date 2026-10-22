package main

import "syscall"

func setSockBuffers(fd uintptr, size int) {
	h := syscall.Handle(fd)
	syscall.SetsockoptInt(h, syscall.SOL_SOCKET, syscall.SO_RCVBUF, size)
	syscall.SetsockoptInt(h, syscall.SOL_SOCKET, syscall.SO_SNDBUF, size)
}
