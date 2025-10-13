// main.go — tsnet-basert lokal HTTP(S) proxy + PAC
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
listen string
name string
verbose bool
)


func vprintf(f string, a ...any) {
if verbose { log.Printf(f, a...) }
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


srv := &tsnet.Server{Hostname: name, AuthKey: authKey, Ephemeral: true}
ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
defer cancel()


if _, err := srv.Up(ctx); err != nil {
log.Fatalf("tsnet oppstart feilet: %v", err)
}
defer srv.Close()


mux := http.NewServeMux()
mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
w.Header().Set("Content-Type", "text/html; charset=utf-8")
fmt.Fprintf(w, `<!doctype html><meta charset="utf-8">
<title>tsnet-browser-proxy</title>
}
