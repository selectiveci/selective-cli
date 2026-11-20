package main

import "errors"

func dupFd(int) (int, error) { return 0, errors.ErrUnsupported }

func dup2(int, int) error { return errors.ErrUnsupported }
