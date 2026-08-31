package ingress

import (
	"bufio"
	"net"
)

type bufReader = bufio.Reader

func newBufReader(c net.Conn) *bufio.Reader { return bufio.NewReader(c) }
