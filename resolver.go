package resolver

import (
	"context"
	"errors"
	"github.com/zofan/go-slist"
	"net"
	"sync"
	"time"
)

const (
	ServerListURL      = `https://public-dns.info/nameservers.txt`
	addressSuffix      = `:53`
	maxServersForSleep = 20
)

var (
	ErrRetryLimit = errors.New(`resolver: retry limit`)
	ErrNoSuchHost = errors.New(`resolver: host not found`)
)

type Resolver struct {
	Servers *slist.List

	DialTimeout      time.Duration
	MaxFails         uint32
	RetryLimit       int
	RetrySleep       time.Duration
	BypassNative     bool
	DisableKeepAlive bool

	mu sync.Mutex
}

func New() *Resolver {
	r := &Resolver{
		DialTimeout:      time.Second * 2, // don't work, look at problem (net.dnsConfig.timeout - net/dnsconfig_unix.go:43)
		RetryLimit:       5,
		RetrySleep:       time.Millisecond * 500,
		MaxFails:         30,
		DisableKeepAlive: true,

		Servers: slist.New(slist.ModeRotate, 3),
	}

	return r
}

func (r *Resolver) LookupIPAddr(host string) (ipList []net.IPAddr, err error) {
	err = r.lookup(host, func(resolver *net.Resolver) (err error) {
		ipList, err = resolver.LookupIPAddr(context.Background(), host)
		return
	})

	if r.BypassNative && err == slist.ErrServerListEmpty {
		ipList, err = net.DefaultResolver.LookupIPAddr(context.Background(), host)
	}

	return ipList, err
}

func (r *Resolver) LookupAddr(ip string) (names []string, err error) {
	err = r.lookup(ip, func(resolver *net.Resolver) (err error) {
		names, err = resolver.LookupAddr(context.Background(), ip)
		return
	})

	if r.BypassNative && err == slist.ErrServerListEmpty {
		names, err = net.DefaultResolver.LookupAddr(context.Background(), ip)
	}

	return names, err
}

func (r *Resolver) LookupNS(host string) (nsList []*net.NS, err error) {
	err = r.lookup(host, func(resolver *net.Resolver) (err error) {
		nsList, err = resolver.LookupNS(context.Background(), host)
		return
	})

	if r.BypassNative && err == slist.ErrServerListEmpty {
		nsList, err = net.DefaultResolver.LookupNS(context.Background(), host)
	}

	return nsList, err
}

func (r *Resolver) LookupTXT(host string) (result []string, err error) {
	err = r.lookup(host, func(resolver *net.Resolver) (err error) {
		result, err = resolver.LookupTXT(context.Background(), host)
		return
	})

	if r.BypassNative && err == slist.ErrServerListEmpty {
		result, err = net.DefaultResolver.LookupTXT(context.Background(), host)
	}

	return result, err
}

func (r *Resolver) LookupCNAME(host string) (cname string, err error) {
	err = r.lookup(host, func(resolver *net.Resolver) (err error) {
		cname, err = resolver.LookupCNAME(context.Background(), host)
		return
	})

	if r.BypassNative && err == slist.ErrServerListEmpty {
		cname, err = net.DefaultResolver.LookupCNAME(context.Background(), host)
	}

	return cname, err
}

func (r *Resolver) LookupMX(host string) (mxList []*net.MX, err error) {
	err = r.lookup(host, func(resolver *net.Resolver) (err error) {
		mxList, err = resolver.LookupMX(context.Background(), host)
		return
	})

	if r.BypassNative && err == slist.ErrServerListEmpty {
		mxList, err = net.DefaultResolver.LookupMX(context.Background(), host)
	}

	return mxList, err
}

func (r *Resolver) lookup(value string, fn func(*net.Resolver) error) error {
	var err error
	attempts := 1

	for {
		server, err := r.Servers.Get()
		if err != nil {
			return err
		}

		stdR := &net.Resolver{
			PreferGo: true,
			Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
				d := net.Dialer{
					Timeout:  r.DialTimeout,
					Resolver: nil,
				}

				if r.DisableKeepAlive {
					d.KeepAlive = -1
				}

				return d.DialContext(ctx, `udp`, server.Addr+addressSuffix)
			},
		}

		err = fn(stdR)
		{
			if err, ok := err.(*net.DNSError); ok && err.IsNotFound {
				r.Servers.MarkGood(server)
				return ErrNoSuchHost
			} else if err == nil {
				r.Servers.MarkGood(server)
				break
			} else {
				r.Servers.MarkBad(server)
			}
		}

		if r.RetryLimit > 0 && attempts >= r.RetryLimit {
			return ErrRetryLimit
		}
		attempts++

		if r.Servers.Count() < maxServersForSleep {
			time.Sleep(r.RetrySleep)
		}
	}

	return err
}
