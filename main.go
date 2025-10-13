// main.go
}


if verbose { fmt.Fprintln(os.Stderr, "Dialer: kobler til", host+":22") }
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
ssh.ECHO: 1,
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
if cmd == "" {
fmt.Fprintln(os.Stderr, "Kjøring i ikke-interaktiv modus uten -c er ikke implementert")
os.Exit(1)
}
if err := sess.Run(cmd); err != nil {
if e, ok := err.(*ssh.ExitError); ok {
os.Exit(e.ExitStatus())
}
io.WriteString(os.Stderr, err.Error()+"\n")
os.Exit(1)
}
}
}
