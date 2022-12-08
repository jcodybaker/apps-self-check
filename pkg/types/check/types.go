package check

import (
	"context"
	"fmt"
	"net"
	"os"
	"sync"
	"time"

	"github.com/google/uuid"
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
	r := net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, "resolver1.opendns.com:53")
		},
	}
	ips, err := r.LookupIP(ctx, "ip4", "myip.opendns.com")
	if err != nil {
		return err
	}
	if len(ips) != 1 {
		return fmt.Errorf("unexpected number of public IPs: %d", len(ips))
	}
	i.Lock()
	defer i.Unlock()
	i.PublicIPv4 = ips[0].String()
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
