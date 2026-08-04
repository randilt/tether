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
	"sync"

	"github.com/mdp/qrterminal/v3"
)

//go:embed web/*
var webFS embed.FS

func main() {
	addr := flag.String("addr", ":8443", "LAN HTTPS listen address (phone + cert)")
	controlAddr := flag.String("control", "127.0.0.1:8444", "localhost HTTPS listen for PC control + view pages")
	flag.Parse()

	certPath, keyPath, err := ensureCert()
	if err != nil {
		log.Fatalf("cert: %v", err)
	}

	hub, err := newHub()
	if err != nil {
		log.Fatalf("webrtc: %v", err)
	}

	webContent, err := fs.Sub(webFS, "web")
	if err != nil {
		log.Fatalf("embed: %v", err)
	}

	phoneBase := phonePublicBase(*addr)
	hub.setPhoneBaseURL(phoneBase)

	phoneMux := http.NewServeMux()
	phoneMux.HandleFunc("/ws", hub.handlePhoneWS)
	phoneMux.HandleFunc("/cert.cer", serveCertDER)
	phoneMux.HandleFunc("/phone", serveFile(webContent, "phone.html"))
	phoneMux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.FileServer(http.FS(webContent)).ServeHTTP(w, r)
			return
		}
		http.Redirect(w, r, "/phone", http.StatusFound)
	})

	controlMux := http.NewServeMux()
	controlMux.HandleFunc("/ws", hub.handleControlListenerWS)
	controlMux.HandleFunc("/control", serveFile(webContent, "control.html"))
	controlMux.HandleFunc("/view", serveFile(webContent, "view.html"))
	controlMux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.FileServer(http.FS(webContent)).ServeHTTP(w, r)
			return
		}
		http.Redirect(w, r, "/control", http.StatusFound)
	})

	code, phoneURL, _ := hub.pairSnapshot()
	printURLs(*addr, *controlAddr, code, phoneBase, phoneURL)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		log.Printf("phone (LAN) listening on https://%s", *addr)
		if err := http.ListenAndServeTLS(*addr, certPath, keyPath, phoneMux); err != nil {
			log.Fatalf("phone listener: %v", err)
		}
	}()
	go func() {
		defer wg.Done()
		log.Printf("control (localhost) listening on https://%s", *controlAddr)
		if err := http.ListenAndServeTLS(*controlAddr, certPath, keyPath, controlMux); err != nil {
			log.Fatalf("control listener: %v", err)
		}
	}()
	wg.Wait()
}

func phonePublicBase(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "https://localhost:8443"
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		if ip := firstLANIPv4(); ip != "" {
			host = ip
		} else {
			host = "localhost"
		}
	}
	return fmt.Sprintf("https://%s", net.JoinHostPort(host, port))
}

func firstLANIPv4() string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			var ip net.IP
			switch v := a.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip == nil || ip.IsLoopback() {
				continue
			}
			ip = ip.To4()
			if ip == nil {
				continue
			}
			return ip.String()
		}
	}
	return ""
}

func serveFile(fsys fs.FS, name string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		b, err := fs.ReadFile(fsys, name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		switch {
		case strings.HasSuffix(name, ".html"):
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
		case strings.HasSuffix(name, ".js"):
			w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		case strings.HasSuffix(name, ".css"):
			w.Header().Set("Content-Type", "text/css; charset=utf-8")
		}
		_, _ = w.Write(b)
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

func printURLs(phoneAddr, controlAddr, pairCode, phoneBase, phoneURL string) {
	fmt.Println()
	fmt.Println("Tether — phone → WebRTC → OBS view pages")
	fmt.Println()
	fmt.Printf("  Phone (LAN):  https://%s/phone\n", listenHostPort(phoneAddr))
	fmt.Printf("  Pair URL:     %s\n", phoneURL)
	fmt.Printf("  Pair code:    %s\n", pairCode)
	fmt.Printf("  Control:      https://%s/control\n", listenHostPort(controlAddr))
	fmt.Printf("  View (OBS):   https://%s/view?id=<deviceId>\n", listenHostPort(controlAddr))
	fmt.Println()
	fmt.Println("  OBS: Browser Source → view URL → Start Virtual Camera → Zoom")
	fmt.Println()
	if phoneBase != "" {
		fmt.Println("  Pair QR:")
		qrterminal.GenerateHalfBlock(phoneURL, qrterminal.L, os.Stdout)
	}
	fmt.Println()
}

func listenHostPort(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	if host == "" || host == "0.0.0.0" {
		host = "0.0.0.0"
	}
	return net.JoinHostPort(host, port)
}
