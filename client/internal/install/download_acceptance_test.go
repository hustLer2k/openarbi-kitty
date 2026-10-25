package install

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type cancelAfterReadTransport struct {
	base      http.RoundTripper
	cancel    context.CancelFunc
	bytesRead int
}

func (t *cancelAfterReadTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	response, err := t.base.RoundTrip(r)
	if err == nil {
		response.Body = &cancelAfterReadBody{ReadCloser: response.Body, transport: t}
	}
	return response, err
}

type cancelAfterReadBody struct {
	io.ReadCloser
	transport *cancelAfterReadTransport
}

func (b *cancelAfterReadBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		b.transport.bytesRead += n
		b.transport.cancel()
	}
	return n, err
}

// Exercise failures after receiving actual TLS response bytes, rather than
// cancelling before a request is dispatched. Trust only this test server's CA;
// certificate verification and the production Download path remain enabled.
func TestHTTPSDownloadFailuresPreservePreviousInstallation(t *testing.T) {
	for _, mode := range []string{"interrupted", "cancelled-midstream", "corrupt-checksum"} {
		t.Run(mode, func(t *testing.T) {
			archive, sum := payload(t)
			o := options(t, sum)
			old, err := InstallArchive(t.Context(), o, archive)
			if err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(archive)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Length", fmt.Sprint(len(data)))
				if mode == "corrupt-checksum" {
					_, _ = w.Write(data)
					return
				}
				_, _ = w.Write(data[:len(data)/2])
				w.(http.Flusher).Flush()
				if mode == "cancelled-midstream" {
					<-r.Context().Done()
				}
				// Returning with an incomplete Content-Length closes the stream.
			}))
			defer server.Close()
			roots := x509.NewCertPool()
			roots.AddCert(server.Certificate())
			transport := http.DefaultTransport.(*http.Transport).Clone()
			transport.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
			previousTransport := http.DefaultTransport
			http.DefaultTransport = transport
			cancelOnRead := &cancelAfterReadTransport{base: transport, cancel: cancel}
			if mode == "cancelled-midstream" {
				http.DefaultTransport = cancelOnRead
			}
			t.Cleanup(func() {
				http.DefaultTransport = previousTransport
				transport.CloseIdleConnections()
			})
			expected := sum
			if mode == "corrupt-checksum" {
				expected = strings.Repeat("0", 64)
			}
			destination := filepath.Join(t.TempDir(), "download.tar.gz")
			err = Download(ctx, server.URL, expected, destination)
			if err == nil {
				t.Fatal("failed HTTPS transfer accepted")
			}
			switch mode {
			case "interrupted":
				if !errors.Is(err, io.ErrUnexpectedEOF) {
					t.Fatalf("expected interrupted response body, got %v", err)
				}
			case "cancelled-midstream":
				if cancelOnRead.bytesRead == 0 {
					t.Fatal("client cancellation occurred without consuming response body bytes")
				}
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("expected cancellation after response began, got %v", err)
				}
			case "corrupt-checksum":
				if !strings.Contains(err.Error(), "checksum mismatch") {
					t.Fatalf("expected completed download checksum rejection, got %v", err)
				}
			default:
				t.Fatal("unknown failure scenario")
			}
			got, err := CurrentRelease(o.Root)
			if err != nil || resolvedPath(t, got) != resolvedPath(t, old) {
				t.Fatalf("previous release changed: %v", err)
			}
			if got, err := os.ReadFile(o.Bin); err != nil || string(got) != "fixture" {
				t.Fatalf("previous entrypoint no longer usable: %v", err)
			}
		})
	}
}
