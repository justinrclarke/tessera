package discover

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/mdns"
)

const Service = "_tessera._tcp"

type Info struct {
	ID    string
	URL   string
	Epoch uint64
	IP    net.IP
	Port  int
}

func Advertise(id, rawURL string, port int, epoch uint64) (func() error, error) {
	ips := LocalIPs()
	txt := []string{
		"id=" + id,
		"epoch=" + strconv.FormatUint(epoch, 10),
		"url=" + rawURL,
	}
	host, _ := os.Hostname()
	svc, err := mdns.NewMDNSService(id, Service, "local.", host+".", port, ips, txt)
	if err != nil {
		return nil, err
	}
	srv, err := mdns.NewServer(&mdns.Config{Zone: svc, Logger: log.New(os.Stderr, "", 0)})
	if err != nil {
		return nil, err
	}
	return srv.Shutdown, nil
}

func Lookup(ctx context.Context) ([]Info, error) {
	entries := make(chan *mdns.ServiceEntry, 8)
	var out []Info
	done := make(chan struct{})
	go func() {
		defer close(done)
		for e := range entries {
			out = append(out, fromEntry(e))
		}
	}()
	q := &mdns.QueryParam{
		Service:     Service,
		Domain:      "local",
		Timeout:     800 * time.Millisecond,
		Entries:     entries,
		DisableIPv6: true,
		Logger:      log.New(os.Stderr, "", 0),
	}
	if err := mdns.QueryContext(ctx, q); err != nil {
		return nil, err
	}
	<-done
	return out, nil
}

func fromEntry(e *mdns.ServiceEntry) Info {
	info := Info{Port: e.Port, IP: e.AddrV4}
	if info.IP == nil {
		info.IP = e.Addr
	}
	for _, f := range e.InfoFields {
		k, v, ok := strings.Cut(f, "=")
		if !ok {
			continue
		}
		switch k {
		case "id":
			info.ID = v
		case "url":
			info.URL = v
		case "epoch":
			n, _ := strconv.ParseUint(v, 10, 64)
			info.Epoch = n
		}
	}
	if info.URL == "" && info.IP != nil && info.Port != 0 {
		info.URL = fmt.Sprintf("http://%s", net.JoinHostPort(info.IP.String(), strconv.Itoa(info.Port)))
	}
	return info
}

func LocalIPs() []net.IP {
	var out []net.IP
	ifaces, err := net.Interfaces()
	if err != nil {
		return []net.IP{net.IPv4(127, 0, 0, 1)}
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
			ipnet, ok := a.(*net.IPNet)
			if !ok || ipnet.IP.To4() == nil {
				continue
			}
			out = append(out, ipnet.IP.To4())
		}
	}
	if len(out) == 0 {
		return []net.IP{net.IPv4(127, 0, 0, 1)}
	}
	return out
}

func FirstURL(port int) string {
	ips := LocalIPs()
	return fmt.Sprintf("http://%s", net.JoinHostPort(ips[0].String(), strconv.Itoa(port)))
}
