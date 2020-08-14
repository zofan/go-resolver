package resolver

import (
	"bufio"
	"context"
	"errors"
	"io"
	"math/rand"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	ModeRandom SelectMode = 1
	ModeRotate SelectMode = 2
	ModeTime   SelectMode = 3

	ServerListURL = `https://public-dns.info/nameservers.txt`
	addressSuffix = `:53`
)

var (
	ErrServerListEmpty = errors.New(`server list is empty`)
	ErrBadMode         = errors.New(`bad mode`)
	ErrRetryLimit      = errors.New(`retry limit`)
	ErrNoSuchHost      = errors.New(`host not found`)
)

type SelectMode int

type Resolver struct {
	servers    []*Server
	badServers []*Server
	srvTicker  *time.Ticker
	lastNS     uint32

	DialTimeout uint32
	MaxFails    uint32
	Mode        SelectMode
	RetryLimit  int
	RetrySleep  time.Duration
	CacheLimit  int
	CacheLife   int

	cache map[string]cacheHost

	mu sync.Mutex
}

type cacheHost struct {
	resolve []net.IPAddr
	lastHit time.Time
}

type Server struct {
	Addr      string
	Fails     uint32
	LastUsage time.Time
}

func New() *Resolver {
	r := &Resolver{
		Mode:        ModeRotate,
		DialTimeout: 1, // don't work, look at problem (net.dnsConfig.timeout - net/dnsconfig_unix.go:43)
		RetryLimit:  5,
		RetrySleep:  time.Millisecond * 500,
		MaxFails:    30,
		CacheLimit:  10000,
		CacheLife:   600,

		cache: make(map[string]cacheHost),
	}

	go func(r *Resolver) {
		t := time.NewTicker(time.Minute * 5)
		for range t.C {
			r.restoreBadServers()
		}
	}(r)

	return r
}

func (r *Resolver) LoadFromString(servers string) error {
	return r.LoadFromReader(strings.NewReader(servers))
}

func (r *Resolver) LoadFromURL(url string) error {
	resp, err := http.Get(url)
	if err != nil {
		return err
	}

	defer resp.Body.Close()

	return r.LoadFromReader(resp.Body)
}

func (r *Resolver) LoadFromReader(reader io.Reader) error {
	r.servers = []*Server{}
	r.badServers = []*Server{}
	r.lastNS = 0

	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		addr := strings.TrimSpace(scanner.Text())
		ip := net.ParseIP(addr)
		if ip == nil || !ip.IsGlobalUnicast() {
			continue
		}

		r.servers = append(r.servers, &Server{Addr: ip.String()})
	}

	return scanner.Err()
}

func (r *Resolver) GetServers() []*Server {
	return r.servers
}

func (r *Resolver) GetServer() (*Server, error) {
	if len(r.servers) == 0 {
		return nil, ErrServerListEmpty
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	switch r.Mode {
	case ModeTime:
		return r.servers[time.Now().Unix()%int64(len(r.servers))], nil
	case ModeRandom:
		return r.servers[rand.Intn(len(r.servers))], nil
	case ModeRotate:
		addr := r.servers[r.lastNS]

		r.lastNS++
		if int(r.lastNS) > len(r.servers)-1 {
			r.lastNS = 0
		}

		return addr, nil
	default:
		return nil, ErrBadMode
	}
}

func (r *Resolver) ResolveHost(host string) ([]net.IPAddr, error) {
	if v, ok := r.cache[host]; ok {
		v.lastHit = time.Now()
		return v.resolve, nil
	}

	var result []net.IPAddr
	err := r.lookup(host, func(resolver *net.Resolver) (err error) {
		result, err = resolver.LookupIPAddr(context.Background(), host)
		return
	})

	if len(r.cache) > r.CacheLimit-1 {
		r.clearCache()
	}
	r.cache[host] = cacheHost{resolve: result, lastHit: time.Now()}

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

func (r *Resolver) lookup(value string, fn func(*net.Resolver) error) error {
	var err error
	attempts := 1

	for {
		server, err := r.GetServer()
		if err != nil {
			return err
		}

		server.LastUsage = time.Now()

		// todo: replace that to the custom dns client
		stdR := &net.Resolver{
			PreferGo: true,
			Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
				d := net.Dialer{
					//Timeout: time.Millisecond * time.Duration(r.DialTimeout),
					//Deadline: time.Now().Add(time.Millisecond * time.Duration(r.DialTimeout)),
					Resolver: nil,
				}
				return d.DialContext(ctx, `udp`, server.Addr+addressSuffix)
			},
		}

		err = fn(stdR)
		{
			if err, ok := err.(*net.DNSError); ok && err.IsNotFound {
				server.Fails = 0
				return ErrNoSuchHost
			} else if err == nil {
				server.Fails = 0
				break
			} else if server.Fails > r.MaxFails {
				r.removeServer(server)
			} else {
				server.Fails++
			}
		}

		if r.RetryLimit > 0 && attempts >= r.RetryLimit {
			return ErrRetryLimit
		}
		attempts++

		if len(r.servers) < 10 {
			time.Sleep(r.RetrySleep)
		}
	}

	return err
}

func (r *Resolver) restoreBadServers() {
	r.mu.Lock()
	defer r.mu.Unlock()

	for i, s := range r.badServers {
		if time.Since(s.LastUsage) > time.Minute*10 {
			r.badServers = append(r.badServers[:i], r.badServers[i+1:]...)

			r.servers = append(r.servers, s)
			s.Fails = 0
		}
	}
}

func (r *Resolver) removeServer(server *Server) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for i, v := range r.servers {
		if v == server {
			r.badServers = append(r.badServers, server)

			r.servers[i] = r.servers[len(r.servers)-1]
			r.servers = r.servers[:len(r.servers)-1]

			r.lastNS = 0
			break
		}
	}
}

func (r *Resolver) clearCache() {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now()

	// remove all long time used hosts
	for h, c := range r.cache {
		if now.Sub(c.lastHit) > time.Second*time.Duration(r.CacheLife) {
			delete(r.cache, h)
		}
	}

	// remove the random key if the previous step didn't give result
	if len(r.cache) > r.CacheLimit-1 {
		for h := range r.cache {
			delete(r.cache, h)
			break
		}
	}
}

func (r *Resolver) Debug() map[string]interface{} {
	d := make(map[string]interface{})

	d[`servers`] = len(r.servers)
	d[`badServers`] = len(r.badServers)
	d[`cacheSize`] = len(r.cache)

	return d
}
