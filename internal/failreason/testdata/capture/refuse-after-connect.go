//go:build ignore

// refuse-after-connect.go stands in for an RTMPS ingest that refuses the RTMP
// application it is asked for, the way Amazon IVS (Kick, Twitch) refuses
// rtmps://<host>/<key>: TLS completes, the RTMP handshake completes, the
// connect command -- which names the application -- is read, and then the
// connection is closed. -mode picks how: notify (a TLS close_notify, then
// FIN), rst (SO_LINGER 0, so a TCP reset), or eof (the raw socket closed with
// the connect command still unread, which the kernel also turns into a reset).
//
//	go run refuse-after-connect.go -mode rst -cert /tmp/cert.pem
//	ffmpeg ... -ca_file /tmp/cert.pem -tls_verify 1 -f flv rtmps://127.0.0.1:19443/<key>
//
// It produced the ffmpeg9-rtmps-refused-after-connect-* fixtures; see README.md.
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"flag"
	"io"
	"log"
	"math/big"
	"net"
	"os"
	"time"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:19443", "")
	mode := flag.String("mode", "notify", "notify | rst | eof")
	certOut := flag.String("cert", "", "write the PEM certificate here")
	flag.Parse()
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "localhost"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}}
	der, _ := x509.CreateCertificate(rand.Reader, tpl, tpl, &k.PublicKey, k)
	cert := tls.Certificate{Certificate: [][]byte{der}, PrivateKey: k}
	if *certOut != "" {
		os.WriteFile(*certOut, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644)
	}
	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatal(err)
	}
	for {
		raw, err := ln.Accept()
		if err != nil {
			log.Fatal(err)
		}
		go func(raw net.Conn) {
			c := tls.Server(raw, &tls.Config{Certificates: []tls.Certificate{cert}})
			c1 := make([]byte, 1537)
			if _, err := io.ReadFull(c, c1); err != nil {
				log.Println("c0c1", err)
				raw.Close()
				return
			}
			s := make([]byte, 1+1536+1536)
			s[0] = 3
			copy(s[1537:], c1[1:])
			c.Write(s)
			c2 := make([]byte, 1536)
			io.ReadFull(c, c2)
			buf := make([]byte, 4096)
			n, _ := c.Read(buf)
			log.Printf("connect command read: %d bytes; closing", n)
			switch *mode {
			case "rst":
				raw.(*net.TCPConn).SetLinger(0)
				raw.Close()
			case "eof":
				raw.Close()
			default:
				c.Close()
			}
		}(raw)
	}
}
