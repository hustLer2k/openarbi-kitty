package roomtransport

import "io"

// DestinationPort is the authorized native app-server forwarding destination.
const DestinationPort = 9091

func bridge(a io.ReadWriteCloser, b io.ReadWriteCloser) {
	done := make(chan struct{}, 1)
	go func() { _, _ = io.Copy(a, b); _ = a.Close(); _ = b.Close(); done <- struct{}{} }()
	_, _ = io.Copy(b, a)
	_ = a.Close()
	_ = b.Close()
	<-done
}
