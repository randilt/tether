package main

import (
	"embed"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
)

//go:embed web/*
var webFS embed.FS

func main() {
	addr := flag.String("addr", ":8443", "HTTPS listen address")
	v4l2 := flag.String("v4l2", "/dev/video10", "v4l2loopback device (empty disables virtual cam)")
	flag.Parse()

	certPath, keyPath, err := ensureCert()
	if err != nil {
		log.Fatalf("cert: %v", err)
	}

	hub, err := newHub(*v4l2)
	if err != nil {
		log.Fatalf("webrtc: %v", err)
	}

	webContent, err := fs.Sub(webFS, "web")
	if err != nil {
		log.Fatalf("embed: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", hub.handleWS)
	mux.HandleFunc("/cert.cer", serveCertDER)
	mux.HandleFunc("/phone", serveFile(webContent, "phone.html"))
	mux.HandleFunc("/control", serveFile(webContent, "control.html"))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.FileServer(http.FS(webContent)).ServeHTTP(w, r)
			return
		}
		http.Redirect(w, r, "/control", http.StatusFound)
	})

	printURLs(*addr, *v4l2)
	log.Printf("listening on https://%s", *addr)
	if err := http.ListenAndServeTLS(*addr, certPath, keyPath, mux); err != nil {
		log.Fatal(err)
	}
}

func serveFile(fsys fs.FS, name string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		data, err := fs.ReadFile(fsys, name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(data)
	}
}

func serveCertDER(w http.ResponseWriter, r *http.Request) {
	data, err := os.ReadFile(certDERPath())
	if err != nil {
		http.Error(w, "cert not found — restart server to regenerate", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/pkix-cert")
	w.Header().Set("Content-Disposition", "attachment; filename=\"tether.cer\"")
	_, _ = w.Write(data)
}

func printURLs(addr, v4l2Device string) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		port = "8443"
		host = ""
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "localhost"
	}

	fmt.Println()
	fmt.Println("Tether — phone → WebRTC → v4l2")
	fmt.Println("────────────────────────────────")
	fmt.Printf("  PC control:  https://%s:%s/control\n", host, port)
	fmt.Printf("  Phone page:  https://%s:%s/phone\n", host, port)
	fmt.Printf("  Install cert: https://%s:%s/cert.cer\n", host, port)
	if v4l2Device != "" {
		fmt.Printf("  Virtual cam: %s\n", v4l2Device)
		if _, err := os.Stat(v4l2Device); err != nil {
			fmt.Printf("  WARNING: %s missing — load v4l2loopback (see README)\n", v4l2Device)
		}
	} else {
		fmt.Println("  Virtual cam: disabled")
	}

	for _, ip := range lanIPv4s() {
		fmt.Printf("  Phone (LAN): https://%s:%s/phone\n", ip, port)
	}
	fmt.Println()
	fmt.Println("First time on iPhone: open /cert.cer, install the profile,")
	fmt.Println("then enable Full Trust (see README).")
	fmt.Println()
}

func lanIPv4s() []string {
	var out []string
	ifaces, err := net.Interfaces()
	if err != nil {
		return out
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip == nil || ip.To4() == nil || ip.IsLinkLocalUnicast() {
				continue
			}
			s := ip.String()
			if strings.HasPrefix(s, "172.17.") { // docker bridge noise
				continue
			}
			out = append(out, s)
		}
	}
	return out
}
