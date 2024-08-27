package checker

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/digitalocean/apps-self-check/pkg/types/check"

	"github.com/miekg/dns"
)

type dialerInsturmentKey struct{}

type dialerInstrument struct {
	measurements chan *check.CheckMeasurement
}

func WithDialerInstrument(ctx context.Context, f check.Check) (measurements []check.CheckMeasurement, err error) {
	var wg sync.WaitGroup
	defer wg.Wait()
	mChannel := make(chan *check.CheckMeasurement)
	defer close(mChannel)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for m := range mChannel {
			if m == nil {
				return
			}
			measurements = append(measurements, *m)
		}
	}()

	ctx = context.WithValue(ctx, dialerInsturmentKey{}, &dialerInstrument{
		measurements: mChannel,
	})
	returnedMeasurements, err := f(ctx)
	for _, m := range returnedMeasurements {
		m := m
		mChannel <- &m
	}
	return
}

func NewInsturmentedTCPDialContext() (func(ctx context.Context, address string) (net.Conn, error), error) {
	dnsConfig, _ := dns.ClientConfigFromFile("/etc/resolv.conf")
	if dnsConfig == nil || len(dnsConfig.Servers) == 0 {
		return nil, errors.New("no dns servers found")
	}
	dnsServer := net.JoinHostPort(dnsConfig.Servers[0], dnsConfig.Port)
	c := new(dns.Client)
	c.Timeout = time.Duration(dnsConfig.Timeout) * time.Second
	d := &net.Dialer{}
	return func(ctx context.Context, address string) (net.Conn, error) {
		v := ctx.Value(dialerInsturmentKey{})
		if v == nil {
			// Not insturmented, fallback to classic dialer
			return d.DialContext(ctx, "tcp", address)
		}
		instrument := v.(*dialerInstrument)
		addr, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		ip := net.ParseIP(addr)
		if ip == nil {
			if !strings.HasSuffix(addr, ".") {
				addr += "."
			}
			dnsQ := new(dns.Msg)
			dnsQ.Id = dns.Id()
			dnsQ.RecursionDesired = true
			dnsQ.Question = []dns.Question{
				{
					Name:   addr,
					Qtype:  dns.TypeA,
					Qclass: dns.ClassINET,
				},
			}
			start := time.Now()
			in, _, err := c.Exchange(dnsQ, dnsServer)
			if err != nil {
				return nil, fmt.Errorf("dns error: %w", err)
			}
			dnsDuration := time.Since(start)
			instrument.measurements <- &check.CheckMeasurement{
				Check: "dns_duration",
				Value: dnsDuration.Seconds(),
			}
		answersLoop:
			for _, answer := range in.Answer {
				switch answer.Header().Rrtype {
				case dns.TypeA:
					ip = answer.(*dns.A).A
					break answersLoop
				case dns.TypeAAAA:
					ip = answer.(*dns.AAAA).AAAA
					break answersLoop
				default:
					continue
				}
			}
		}
		if ip == nil {
			return nil, errors.New("dns error: no addresses found")
		}
		start := time.Now()
		conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(ip.String(), port))
		if err != nil {
			return nil, err
		}
		dialDuration := time.Since(start)
		instrument.measurements <- &check.CheckMeasurement{
			Check: "connect_duration",
			Value: dialDuration.Seconds(),
		}
		return conn, nil
	}, nil
}
