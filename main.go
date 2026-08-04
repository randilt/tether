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
	controlAddr := flag.String("control", "127.0.0.1:8444", "localhost HTTPS listen for PC control UI")
	v4l2 := flag.String("v4l2", "/dev/video10", "v4l2loopback device (empty disables virtual cam)")
	audio := flag.String("audio", "pulse:default", "audio sink: pulse:default | alsa:default | alsa:hw:Loopback,0,0 | empty disables")
	flag.Parse()

	certPath, keyPath, err := ensureCert()
	if err != nil {
		log.Fatalf("cert: %v", err)
	}

	hub, err := newHub(*v4l2, *audio)
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
	controlMux.HandleFunc("/ws", hub.handleControlWS)
	controlMux.HandleFunc("/control", serveFile(webContent, "control.html"))
	controlMux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.FileServer(http.FS(webContent)).ServeHTTP(w, r)
			return
		}
		http.Redirect(w, r, "/control", http.StatusFound)
	})

	code, phoneURL, _ := hub.pairSnapshot()
	printURLs(*addr, *controlAddr, *v4l2, *audio, code, phoneBase, phoneURL)

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
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		port = "8443"
	}
	if lan := lanIPv4s(); len(lan) > 0 {
		return fmt.Sprintf("https://%s:%s", lan[0], port)
	}
	return fmt.Sprintf("https://localhost:%s", port)
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

func printURLs(phoneAddr, controlAddr, v4l2Device, audioDest, pairCode, phoneBase, phoneURL string) {
	fmt.Println()
	fmt.Println("Tether — phone → WebRTC → v4l2 / audio")
	fmt.Println("──────────────────────────────────────")
	fmt.Printf("  Pairing code: %s  (expires in %s or after first use)\n", pairCode, pairCodeTTL)
	fmt.Printf("  PC control:   https://%s/control  (localhost only)\n", controlAddr)
	fmt.Printf("  Phone URL:    %s\n", phoneURL)
	fmt.Printf("  Install cert: %s/cert.cer\n", phoneBase)
	if v4l2Device != "" {
		fmt.Printf("  Virtual cam:  %s\n", v4l2Device)
		if _, err := os.Stat(v4l2Device); err != nil {
			fmt.Printf("  ERROR: %s missing — control page shows fix command\n", v4l2Device)
			fmt.Printf("         %s\n", v4l2FixCommand(v4l2Device))
		}
	} else {
		fmt.Println("  Virtual cam:  disabled")
	}
	if audioDest != "" {
		fmt.Printf("  Audio sink:   %s\n", audioDest)
	} else {
		fmt.Println("  Audio sink:   disabled")
	}

	_, phonePort, _ := net.SplitHostPort(strings.TrimPrefix(strings.TrimPrefix(phoneBase, "https://"), "http://"))
	if phonePort == "" {
		phonePort = "8443"
	}
	for _, ip := range lanIPv4s() {
		alt := fmt.Sprintf("https://%s:%s/phone?t=%s", ip, phonePort, pairCode)
		if alt == phoneURL {
			continue
		}
		fmt.Printf("  Phone (alt):  %s\n", alt)
	}

	fmt.Println()
	fmt.Println("Scan with phone camera (pairing URL):")
	qrterminal.GenerateWithConfig(phoneURL, qrterminal.Config{
		Level:      qrterminal.M,
		Writer:     os.Stdout,
		HalfBlocks: true,
		QuietZone:  1,
	})
	fmt.Println("Codes expire after 10 minutes or first successful pair; control page updates live.")
	fmt.Println("Open control on this PC only (not from other LAN devices).")
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
			if strings.HasPrefix(s, "172.17.") {
				continue
			}
			out = append(out, s)
		}
	}
	return out
}
