// main.go
package main

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"golang.org/x/crypto/ssh"
	"tailscale.com/tsnet"
)

func readKey(path string) (ssh.Signer, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	// Støtt ukryptert PEM. (Legg evt. inn passphrase-støtte ved behov.)
	p, _ := pem.Decode(b)
	if p == nil {
		return nil, fmt.Errorf("fant ikke PEM i %s", path)
	}
	if strings.Contains(p.Headers["Proc-Type"], "ENCRYPTED") {
		return nil, fmt.Errorf("nøkkelen ser kryptert ut; passphrase-støtte ikke implementert")
	}
	priv, err := x509.ParsePKCS1PrivateKey(p.Bytes)
	if err != nil {
		k, err2 := x509.ParsePKCS8PrivateKey(p.Bytes)
		if err2 != nil {
			return nil, err
		}
		return ssh.NewSignerFromKey(k)
	}
	return ssh.NewSignerFromKey(priv)
}

func main() {
	var (
		login    string
		keyPath  string
		password string
		cmd      string
	)
	flag.StringVar(&login, "l", "", "login i formatet user@host (host = Tailnet-navn eller 100.x.y.z)")
	flag.StringVar(&keyPath, "i", "", "sti til privatnøkkel (PEM)")
	flag.StringVar(&password, "P", "", "passord (hvis du ikke bruker nøkkel)")
	flag.StringVar(&cmd, "c", "", "kommando som skal kjøres (tom = interaktiv shell)")
	flag.Parse()

	if login == "" || !strings.Contains(login, "@") {
		fmt.Fprintln(os.Stderr, "Bruk: ssh-tail -l user@host [-i key.pem | -P pass] [-c \"kommando\"]")
		os.Exit(2)
	}

	user := strings.SplitN(login, "@", 2)[0]
	host := strings.SplitN(login, "@", 2)[1]

	// Start embedded Tailscale
	srv := &tsnet.Server{
		// Gi den et nøytralt navn i tailnet (vises som en node mens exe kjører)
		Hostname: "ssh-tail-client",
		// Bruk env TS_AUTHKEY for login. Ephemeral anbefales.
		AuthKey: os.Getenv("TS_AUTHKEY"),
		// Userspace nettstack for å slippe admin/drivere
		Ephemeral: true,
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if _, err := srv.Up(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "Tailscale/tsnet feilet ved oppstart:", err)
		os.Exit(1)
	}
	defer srv.Close()

	// SSH auth
	var authMethods []ssh.AuthMethod
	if keyPath != "" {
		signer, err := readKey(keyPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, "Les nøkkel feilet:", err)
			os.Exit(1)
		}
		authMethods = append(authMethods, ssh.PublicKeys(signer))
	}
	if password != "" {
		authMethods = append(authMethods, ssh.Password(password))
	}
	if len(authMethods) == 0 {
		fmt.Fprintln(os.Stderr, "Du må oppgi -i key.pem eller -P passord")
		os.Exit(2)
	}

	cfg := &ssh.ClientConfig{
		User:            user,
		Auth:            authMethods,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), // Forenklet; bytt til known_hosts for bedre sikkerhet
		Timeout:         10 * time.Second,
	}

	// Dial port 22 over Tailnet
	dialer := srv.Dial
	conn, err := dialer(ctx, "tcp", host+":22")
	if err != nil {
		fmt.Fprintln(os.Stderr, "Dial Tailnet TCP feilet:", err)
		os.Exit(1)
	}
	sshConn, chans, reqs, err := ssh.NewClientConn(conn, host+":22", cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "SSH handshake feilet:", err)
		os.Exit(1)
	}
	client := ssh.NewClient(sshConn, chans, reqs)
	defer client.Close()

	sess, err := client.NewSession()
	if err != nil {
		fmt.Fprintln(os.Stderr, "Ny SSH-session feilet:", err)
		os.Exit(1)
	}
	defer sess.Close()

	sess.Stdin = os.Stdin
	sess.Stdout = os.Stdout
	sess.Stderr = os.Stderr

	modes := ssh.TerminalModes{
		ssh.ECHO:          1,
		ssh.TTY_OP_ISPEED: 14400,
		ssh.TTY_OP_OSPEED: 14400,
	}
	if cmd == "" && isTTY(os.Stdin) && isTTY(os.Stdout) {
		_ = sess.RequestPty("xterm-256color", 40, 120, modes)
		if err := sess.Shell(); err != nil {
			fmt.Fprintln(os.Stderr, "Start shell feilet:", err)
			os.Exit(1)
		}
		_ = sess.Wait()
	} else {
		if err := sess.Run(cmd); err != nil {
			// Propager exit code om mulig
			if e, ok := err.(*ssh.ExitError); ok {
				os.Exit(e.ExitStatus())
			}
			// Hvis ikke, bare print feilen
			io.WriteString(os.Stderr, err.Error()+"\n")
			os.Exit(1)
		}
	}
}

func isTTY(f *os.File) bool {
	// Minimal sjekk; på Windows funker vanligvis ok. For enkelhet returner true hvis det er en konsoll.
	fi, _ := f.Stat()
	return (fi.Mode() & os.ModeCharDevice) != 0
}
