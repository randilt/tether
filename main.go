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

	"github.com/mdp/qrterminal/v3"
)

//go:embed web/*
var webFS embed.FS

func main() {
	addr := flag.String("addr", ":8443", "HTTPS listen address")
	v4l2 := flag.String("v4l2", "/dev/video10", "v4l2loopback device (empty disables virtual cam)")
	audio := flag.String("audio", "pulse:default", "audio sink: pulse:default | alsa:default | alsa:hw:Loopback,0,0 | empty disables")
	flag.Parse()

	certPath, keyPath, err := ensureCert()
	if err != nil {
		log.Fatalf("cert: %v", err)
	}

	pairCode, err := generatePairCode()
	if err != nil {
		log.Fatalf("pair code: %v", err)
	}

	hub, err := newHub(*v4l2, *audio, pairCode)
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

	printURLs(*addr, *v4l2, *audio, pairCode)
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

func printURLs(addr, v4l2Device, audioDest, pairCode string) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		port = "8443"
		host = ""
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "localhost"
	}

	lan := lanIPv4s()
	scanHost := host
	if len(lan) > 0 {
		scanHost = lan[0]
	}
	phoneURL := fmt.Sprintf("https://%s:%s/phone?t=%s", scanHost, port, pairCode)

	fmt.Println()
	fmt.Println("Tether — phone → WebRTC → v4l2 / audio")
	fmt.Println("──────────────────────────────────────")
	fmt.Printf("  Pairing code: %s\n", pairCode)
	fmt.Printf("  PC control:   https://%s:%s/control\n", host, port)
	fmt.Printf("  Phone URL:    %s\n", phoneURL)
	fmt.Printf("  Install cert: https://%s:%s/cert.cer\n", host, port)
	if v4l2Device != "" {
		fmt.Printf("  Virtual cam:  %s\n", v4l2Device)
		if _, err := os.Stat(v4l2Device); err != nil {
			fmt.Printf("  WARNING: %s missing — load v4l2loopback (see README)\n", v4l2Device)
		}
	} else {
		fmt.Println("  Virtual cam:  disabled")
	}
	if audioDest != "" {
		fmt.Printf("  Audio sink:   %s\n", audioDest)
	} else {
		fmt.Println("  Audio sink:   disabled")
	}
	for _, ip := range lan {
		if ip == scanHost {
			continue
		}
		fmt.Printf("  Phone (alt):  https://%s:%s/phone?t=%s\n", ip, port, pairCode)
	}

	fmt.Println()
	fmt.Println("Scan with phone camera (pairing URL):")
	qrterminal.GenerateWithConfig(phoneURL, qrterminal.Config{
		Level:      qrterminal.M,
		Writer:     os.Stdout,
		HalfBlocks: true,
		QuietZone:  1,
	})
	fmt.Println("Phones need this URL/code. New code each restart.")
	fmt.Println("First time on iPhone: open /cert.cer, install + Full Trust (see README).")
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
