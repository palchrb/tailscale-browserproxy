// main.go — tsnet-based local HTTP(S) proxy + PAC for Tailnet access without installation
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/textproto"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"tailscale.com/tsnet"
)

var (
	listen  string
	name    string
	verbose bool
)

const homeTemplate = `<!doctype html><meta charset="utf-8">
<title>tsnet-browser-proxy</title>
<h1>tsnet-browser-proxy</h1>
<p>Running as <b>%s</b>. Set browser proxy to <code>http://%s</code> (HTTP and HTTPS).</p>
<p>Optional PAC: <code>http://%s/proxy.pac</code></p>
<pre>SET TS_AUTHKEY=tskey-ephemeral-XXXX
%s -v
Chrome: chrome.exe --proxy-server="http://%s"
Firefox: Settings → Network → Manual proxy → HTTP Proxy: 127.0.0.1  Port: 8384  (check HTTPS as well)</pre>
`

const pacTemplate = `function FindProxyForURL(url, host) {
  var p = "PROXY %s:%s";
  if (shExpMatch(host, "*.ts.net")) return p;
  if (isInNet(host, "100.64.0.0", "255.192.0.0")) return p;
  return "DIRECT";
}
`

func vprintf(f string, a ...any) {
	if verbose {
		log.Printf(f, a...)
	}
}

func main() {
	flag.StringVar(&listen, "listen", "127.0.0.1:8384", "address for local proxy & PAC")
	flag.StringVar(&name, "name", "tsnet-browser-proxy", "tsnet Hostname in tailnet")
	flag.BoolVar(&verbose, "v", false, "verbose logging")
	flag.Parse()

	authKey := os.Getenv("TS_AUTHKEY")
	if authKey == "" {
		log.Fatal("TS_AUTHKEY missing from environment")
	}

	// Start embedded Tailscale node
	srv := &tsnet.Server{
		Hostname:  name,
		AuthKey:   authKey,
		Ephemeral: true,
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if _, err := srv.Up(ctx); err != nil {
		log.Fatalf("tsnet startup failed: %v", err)
	}
	defer srv.Close()

	// One HTTP server that serves both / and /proxy.pac, and also acts as proxy handler
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// If this is a normal HTTP call to our own server (no absolute-form URL),
		// handle / and /proxy.pac here. Proxy requests from browsers use absolute-form.
		if r.Method != http.MethodConnect && (r.URL.Scheme == "" || r.URL.Host == "") {
			switch r.URL.Path {
			case "/":
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				fmt.Fprintf(w, homeTemplate, name, listen, listen, os.Args[0], listen)
				return
			case "/proxy.pac":
				w.Header().Set("Content-Type", "application/x-ns-proxy-autoconfig")
				host, port, _ := net.SplitHostPort(listen)
				if host == "" {
					host = "127.0.0.1"
				}
				if port == "" {
					port = "8384"
				}
				fmt.Fprintf(w, pacTemplate, host, port)
				return
			default:
				http.NotFound(w, r)
				return
			}
		}

		// Proxy mode
		if r.Method == http.MethodConnect {
			handleConnect(w, r, srv)
			return
		}
		handleHTTPForward(w, r, srv)
	})

	server := &http.Server{
		Addr:         listen,
		Handler:      handler,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
	}

	log.Printf("HTTP proxy and PAC at http://%s  (PAC: /proxy.pac)", listen)

	// Start server (blocks until signal)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("ListenAndServe failed: %v", err)
	}
}

// Handle HTTP CONNECT → establish raw TCP tunnel via tsnet.Dial
func handleConnect(w http.ResponseWriter, r *http.Request, srv *tsnet.Server) {
	target := r.Host
	vprintf("CONNECT %s", target)

	// Hijack HTTP connection for raw TCP
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "proxy supports hijacking only", http.StatusInternalServerError)
		return
	}
	clientConn, buf, err := hj.Hijack()
	if err != nil {
		return
	}
	defer func() {
		_ = clientConn.Close()
	}()

	// Dial Tailnet
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	tsConn, err := srv.Dial(ctx, "tcp", target)
	if err != nil {
		_, _ = io.WriteString(clientConn, "HTTP/1.1 502 Bad Gateway\r\n\r\n")
		return
	}
	defer tsConn.Close()

	// OK to client
	_, _ = io.WriteString(clientConn, "HTTP/1.1 200 Connection Established\r\n\r\n")

	// Pump both directions
	errCh := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(tsConn, buf)
		// Try half-close if possible, otherwise just close
		type closeWriter interface{ CloseWrite() error }
		if cw, ok := tsConn.(closeWriter); ok {
			_ = cw.CloseWrite()
		} else {
			_ = tsConn.Close()
		}
		errCh <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(clientConn, tsConn)
		_ = clientConn.Close()
		errCh <- struct{}{}
	}()
	<-errCh
}

// Handle plain HTTP via absolute-form URI: forward request through tsnet.Dial
func handleHTTPForward(w http.ResponseWriter, r *http.Request, srv *tsnet.Server) {
	if r.URL.Scheme == "" || r.URL.Host == "" {
		http.Error(w, "Expected absolute-form URI through proxy", http.StatusBadRequest)
		return
	}

	addr := r.URL.Host
	if !strings.Contains(addr, ":") {
		if r.URL.Scheme == "https" {
			addr += ":443"
		} else {
			addr += ":80"
		}
	}
	vprintf("HTTP %s %s", r.Method, r.URL.String())

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	backend, err := srv.Dial(ctx, "tcp", addr)
	if err != nil {
		http.Error(w, "502 Bad Gateway", http.StatusBadGateway)
		return
	}
	defer backend.Close()

	// Write request line in origin-form (path + query)
	reqLine := fmt.Sprintf("%s %s HTTP/1.1\r\n", r.Method, r.URL.RequestURI())
	if _, err := io.WriteString(backend, reqLine); err != nil {
		http.Error(w, "502", http.StatusBadGateway)
		return
	}

	// Copy headers, but set Host correctly; remove Proxy-Connection/Connection that might cause issues
	h := make(http.Header)
	for k, vv := range r.Header {
		for _, v := range vv {
			// Filter out proxy-specific headers
			if strings.EqualFold(k, "Proxy-Connection") {
				continue
			}
			h.Add(k, v)
		}
	}
	h.Set("Host", r.URL.Host)
	h.Del("Proxy-Connection")

	// Write headers
	tp := textproto.MIMEHeader(h)
	for k, vv := range tp {
		for _, v := range vv {
			fmt.Fprintf(backend, "%s: %s\r\n", k, v)
		}
	}
	io.WriteString(backend, "\r\n")

	// Body → backend
	if r.Body != nil {
		_, _ = io.Copy(backend, r.Body)
	}

	// Read response from backend and send to client
	br := bufio.NewReader(backend)
	resp, err := http.ReadResponse(br, r)
	if err != nil {
		http.Error(w, "502", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	for k, vv := range resp.Header {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}
