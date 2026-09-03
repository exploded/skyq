package alpaca

import "syscall"

// setSocketReuse marks a socket SO_REUSEADDR before bind, so alpaca-switch
// and skyq can share the UDP discovery port as the Alpaca spec directs.
func setSocketReuse(fd uintptr) error {
	return syscall.SetsockoptInt(syscall.Handle(fd), syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)
}
