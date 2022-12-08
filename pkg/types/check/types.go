package check

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/miekg/dns"
)

type Instance struct {
	DatabaseID int64
	UUID       string
	AppID      string
	Hostname   string
	PublicIPv4 string
	Labels     map[string]string
	StartedAt  time.Time
	StoppedAt  time.Time
	sync.Mutex
}

func NewInstance() (*Instance, error) {
	uuid, err := uuid.NewRandom()
	if err != nil {
		return nil, err
	}
	hostname, err := os.Hostname()
	if err != nil {
		return nil, err
	}
	return &Instance{
		UUID:      uuid.String(),
		Hostname:  hostname,
		StartedAt: time.Now(),
	}, nil
}

func (i *Instance) UpdatePublicIPv4(ctx context.Context) error {
	// dig +short myip.opendns.com @resolver1.opendns.com
	m := new(dns.Msg)
	m.SetQuestion("myip.opendns.com.", dns.TypeA)
	c := dns.Client{
		Net: "tcp", // Forcing TCP mode overrides Docker's DNS intercept for local dev.
	}
	in, _, err := c.Exchange(m, "resolver1.opendns.com:53")
	if err != nil {
		return err
	}
	if len(in.Answer) != 1 {
		return fmt.Errorf("unexpected number of public IPs: %d", len(in.Answer))
	}
	a, ok := in.Answer[0].(*dns.A)
	if !ok {
		return fmt.Errorf("unexpected answer type: %T", in.Answer[0])
	}

	i.Lock()
	defer i.Unlock()

	i.PublicIPv4 = a.A.String()
	return nil
}

type CheckError struct {
	Check string
	Error string
}

type CheckResults struct {
	Instance     *Instance
	TS           time.Time
	Errors       []CheckError
	Measurements []CheckMeasurement
}

type CheckMeasurement struct {
	Check string
	Value float64
}

// Check describes a function which validates this app.
type Check func(context.Context) ([]CheckMeasurement, error)
