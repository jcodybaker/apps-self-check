package checker

import (
	"errors"
	"net"
	"os"
	"sync"
	"time"

	"github.com/miekg/dns"
)

var dnsClientConfig = sync.OnceValues(func() (*dns.ClientConfig, error) {
	dnsConfig, err := dns.ClientConfigFromFile("/etc/resolv.conf")
	if err != nil {
		return nil, err
	}
	if dnsServer := os.Getenv("DNS_SERVER"); dnsServer != "" {
		host, port, err := net.SplitHostPort(dnsServer)
		if err == nil && port != "" {
			dnsConfig.Port = port
		} else {
			// Assume that if SplitHostPort fails the port is missing and append the configured port.
			host = dnsServer
		}
		dnsConfig.Servers = []string{host}
	}
	if len(dnsConfig.Servers) == 0 {
		return nil, errors.New("no dns servers found")
	}
	return dnsConfig, nil
})

func dnsExchange(m *dns.Msg) (*dns.Msg, time.Duration, error) {
	dnsConfig, err := dnsClientConfig()
	if err != nil {
		return nil, 0, err
	}
	c := new(dns.Client)
	c.Timeout = time.Duration(dnsConfig.Timeout) * time.Second
	return c.Exchange(m, net.JoinHostPort(dnsConfig.Servers[0], dnsConfig.Port))
}
