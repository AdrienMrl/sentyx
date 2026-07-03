//go:build linux || darwin

package main

import "syscall"

func syncAll() error {
	syscall.Sync()
	return nil
}
