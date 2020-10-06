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
	ServerListURL = `https://public-dns.info/nameservers.txt`
	addressSuffix = `:53`
)

var (
	ErrRetryLimit = errors.New(`resolver: retry limit`)
	ErrNoSuchHost = errors.New(`resolver: host not found`)
)

type Resolver struct {
	Servers *slist.List

	DialTimeout time.Duration
	MaxFails    uint32
	RetryLimit  int
	RetrySleep  time.Duration
	CacheLimit  int
	CacheLife   time.Duration

	cache map[string]cacheHost

	mu sync.Mutex
}

type cacheHost struct {
	addr    []net.IPAddr
	lastHit time.Time
}

func New() *Resolver {
	r := &Resolver{
		DialTimeout: time.Second, // don't work, look at problem (net.dnsConfig.timeout - net/dnsconfig_unix.go:43)
		RetryLimit:  5,
		RetrySleep:  time.Millisecond * 500,
		MaxFails:    30,
		CacheLimit:  65535,
		CacheLife:   time.Second * 300,

		Servers: slist.New(slist.ModeRotate, 3, time.Minute*10),

		cache: make(map[string]cacheHost),
	}

	return r
}

func (r *Resolver) ResolveHost(host string) ([]net.IPAddr, error) {
	r.mu.Lock()
	if v, ok := r.cache[host]; ok && r.CacheLimit > 0 {
		v.lastHit = time.Now()
		r.mu.Unlock()
		return v.addr, nil
	}
	r.mu.Unlock()

	var result []net.IPAddr
	err := r.lookup(host, func(resolver *net.Resolver) (err error) {
		result, err = resolver.LookupIPAddr(context.Background(), host)
		return
	})

	if r.CacheLimit > 0 {
		r.mu.Lock()
		if len(r.cache) > r.CacheLimit-1 {
			r.clearCache()
		}
		r.cache[host] = cacheHost{addr: result, lastHit: time.Now()}
		r.mu.Unlock()
	}

	return result, err
}

func (r *Resolver) ReverseIP(ip string) ([]string, error) {
	var result []string

	err := r.lookup(ip, func(resolver *net.Resolver) (err error) {
		result, err = resolver.LookupAddr(context.Background(), ip)
		return
	})

	return result, err
}

func (r *Resolver) LookupNS(host string) ([]*net.NS, error) {
	var result []*net.NS

	err := r.lookup(host, func(resolver *net.Resolver) (err error) {
		result, err = resolver.LookupNS(context.Background(), host)
		return
	})

	return result, err
}

func (r *Resolver) LookupTXT(host string) ([]string, error) {
	var result []string

	err := r.lookup(host, func(resolver *net.Resolver) (err error) {
		result, err = resolver.LookupTXT(context.Background(), host)
		return
	})

	return result, err
}

func (r *Resolver) lookup(value string, fn func(*net.Resolver) error) error {
	var err error
	attempts := 1

	for {
		server, err := r.Servers.Get()
		if err != nil {
			return err
		}

		// todo: replace that to the custom dns client with proxy
		stdR := &net.Resolver{
			PreferGo: true,
			Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
				d := net.Dialer{
					Timeout: r.DialTimeout,
					//Deadline: time.Now().Add(time.Millisecond * time.Duration(r.DialTimeout)),
					Resolver: nil,
				}
				return d.DialContext(ctx, `udp`, server.Addr+addressSuffix)
			},
		}

		err = fn(stdR)
		{
			if err, ok := err.(*net.DNSError); ok && err.IsNotFound {
				server.Good()
				return ErrNoSuchHost
			} else if err == nil {
				server.Good()
				break
			} else {
				r.Servers.MarkBad(server)
			}
		}

		if r.RetryLimit > 0 && attempts >= r.RetryLimit {
			return ErrRetryLimit
		}
		attempts++

		if r.Servers.Count() < 10 {
			time.Sleep(r.RetrySleep)
		}
	}

	return err
}

func (r *Resolver) clearCache() {
	now := time.Now()

	// remove all long time used hosts
	for h, c := range r.cache {
		if now.Sub(c.lastHit) >= r.CacheLife {
			delete(r.cache, h)
		}
	}

	// remove the random key if the previous step didn't give result
	if len(r.cache) >= r.CacheLimit {
		for h := range r.cache {
			delete(r.cache, h)
			break
		}
	}
}
