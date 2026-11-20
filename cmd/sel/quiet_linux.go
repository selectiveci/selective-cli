package main

import "syscall"

func dupFd(fd int) (int, error) { return syscall.Dup(fd) }

func dup2(oldfd, newfd int) error { return syscall.Dup3(oldfd, newfd, 0) }
