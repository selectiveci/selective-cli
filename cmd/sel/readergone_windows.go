package main

import "os"

func readerGone(*os.File) bool { return false }
