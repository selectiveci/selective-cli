package main

import "os"

func readerGone(*os.File) bool { return false }

func eioMeansReaderGone(*os.File, bool) bool { return false }
