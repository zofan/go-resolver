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
	ErrServerListEmpty = errors.New(`resolver: server list is empty`)
	ErrBadMode         = errors.New(`resolver: bad mode`)
	ErrRetryLimit      = errors.New(`resolver: retry limit`)
	ErrNoSuchHost      = errors.New(`resolver: host not found`)
)

type SelectMode int

type Resolver struct {
	servers    []*Server
	badServers []*Server
	srvTicker  *time.Ticker
	lastNS     uint32

	DialTimeout time.Duration
	MaxFails    uint32
	Mode        SelectMode
	RetryLimit  int
	RetrySleep  time.Duration
	CacheLimit  int
	CacheLife   time.Duration
	RestoreTime time.Duration

	cache map[string]cacheHost

	mu sync.Mutex
}

type cacheHost struct {
	addr    []net.IPAddr
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
		DialTimeout: time.Second, // don't work, look at problem (net.dnsConfig.timeout - net/dnsconfig_unix.go:43)
		RetryLimit:  5,
		RetrySleep:  time.Millisecond * 500,
		MaxFails:    30,
		CacheLimit:  65535,
		CacheLife:   time.Second * 300,
		RestoreTime: time.Minute * 15,

		cache: make(map[string]cacheHost),
	}

	go func() {
		for range time.Tick(time.Minute) {
			r.restoreServers()
		}
	}()

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
	r.mu.Lock()
	defer r.mu.Unlock()

	r.servers = []*Server{}
	r.badServers = []*Server{}
	r.lastNS = 0

	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		r.AddServer(strings.TrimSpace(scanner.Text()))
	}

	return scanner.Err()
}

func (r *Resolver) AddServer(addr string) {
	ip := net.ParseIP(addr)
	if ip == nil || !ip.IsGlobalUnicast() {
		return
	}

	r.servers = append(r.servers, &Server{Addr: ip.String()})
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
		server, err := r.GetServer()
		if err != nil {
			return err
		}

		server.LastUsage = time.Now()

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
				server.Fails = 0
				return ErrNoSuchHost
			} else if err == nil {
				server.Fails = 0
				break
			} else if server.Fails > r.MaxFails {
				r.disableServer(server)
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

func (r *Resolver) restoreServers() {
	r.mu.Lock()
	defer r.mu.Unlock()

	for i, s := range r.badServers {
		if time.Since(s.LastUsage) > r.RestoreTime {
			r.badServers[i] = r.badServers[len(r.badServers)-1]
			r.badServers = r.badServers[:len(r.badServers)-1]

			r.servers = append(r.servers, s)

			s.Fails = 0
		}
	}
}

func (r *Resolver) disableServer(server *Server) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for i, s := range r.servers {
		if s == server {
			r.servers[i] = r.servers[len(r.servers)-1]
			r.servers = r.servers[:len(r.servers)-1]

			r.badServers = append(r.badServers, s)

			r.lastNS = 0
			break
		}
	}
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

func (r *Resolver) Debug() map[string]interface{} {
	d := make(map[string]interface{})

	d[`servers`] = len(r.servers)
	d[`badServers`] = len(r.badServers)
	d[`cacheSize`] = len(r.cache)

	return d
}
