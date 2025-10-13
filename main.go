// main.go — tsnet-basert lokal HTTP(S) proxy + PAC for Tailnet-tilgang uten installasjon
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
<p>Kjører som <b>%s</b>. Sett nettleser-proxy til <code>http://%s</code> (HTTP og HTTPS).</p>
<p>Valgfritt PAC: <code>http://%s/proxy.pac</code></p>
<pre>SET TS_AUTHKEY=tskey-ephemeral-XXXX
%s -v
Chrome: chrome.exe --proxy-server="http://%s"
Firefox: Settings → Network → Manual proxy → HTTP Proxy: 127.0.0.1  Port: 8384  (hake for HTTPS også)</pre>
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
	flag.StringVar(&listen, "listen", "127.0.0.1:8384", "adresse for lokal proxy & PAC")
	flag.StringVar(&name, "name", "tsnet-browser-proxy", "tsnet Hostname i tailnet")
	flag.BoolVar(&verbose, "v", false, "verbose logging")
	flag.Parse()

	authKey := os.Getenv("TS_AUTHKEY")
	if authKey == "" {
		log.Fatal("TS_AUTHKEY mangler i miljøet")
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
		log.Fatalf("tsnet oppstart feilet: %v", err)
	}
	defer srv.Close()

	// Én HTTP-server som både viser / og /proxy.pac, og fungerer som proxy-handler
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Hvis dette er en vanlig HTTP-kall til vår egen server (ingen absolute-form URL),
		// så betjen / og /proxy.pac her. Proxy-forespørsler fra nettleser bruker absolute-form.
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

		// Proxy-modus
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

	log.Printf("HTTP proxy og PAC på http://%s  (PAC: /proxy.pac)", listen)

	// Start server (blokkerer til signal)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("ListenAndServe feilet: %v", err)
	}
}

// Handle HTTP CONNECT → opprett rå TCP-tunnel via tsnet.Dial
func handleConnect(w http.ResponseWriter, r *http.Request, srv *tsnet.Server) {
	target := r.Host
	vprintf("CONNECT %s", target)

	// Hijack HTTP-tilkoblingen for rå TCP
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

	// OK til klient
	_, _ = io.WriteString(clientConn, "HTTP/1.1 200 Connection Established\r\n\r\n")

	// Pump begge veier
	errCh := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(tsConn, buf)
		// Forsøk half-close om mulig, ellers bare close
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

// Handle plain HTTP gjennom absolute-form URI: videresend request via tsnet.Dial
func handleHTTPForward(w http.ResponseWriter, r *http.Request, srv *tsnet.Server) {
	if r.URL.Scheme == "" || r.URL.Host == "" {
		http.Error(w, "Forventet absolute-form URI gjennom proxy", http.StatusBadRequest)
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

	// Skriv request line i origin-form (path + query)
	reqLine := fmt.Sprintf("%s %s HTTP/1.1\r\n", r.Method, r.URL.RequestURI())
	if _, err := io.WriteString(backend, reqLine); err != nil {
		http.Error(w, "502", http.StatusBadGateway)
		return
	}

	// Kopier headers, men sett Host korrekt; fjern Proxy-Connection/Connection som kan skape krøll
	h := make(http.Header)
	for k, vv := range r.Header {
		for _, v := range vv {
			// Filtrer ut proxy-spesifikke headers
			if strings.EqualFold(k, "Proxy-Connection") {
				continue
			}
			h.Add(k, v)
		}
	}
	h.Set("Host", r.URL.Host)
	h.Del("Proxy-Connection")

	// Skriv headers
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

	// Les svar fra backend og send til klient
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
