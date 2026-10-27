//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package daemon

import (
	"errors"
	"net"
)

func listenUnix(string) (net.Listener, func(), error) {
	return nil, func() {}, errors.New("Unix sockets are not supported on this operating system")
}
