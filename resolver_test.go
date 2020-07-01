package resolver

import (
	"fmt"
	"sync"
	"testing"
)

func TestGetServerRandom(t *testing.T) {
	r := New()

	err := r.LoadFromString("8.8.8.8\n1.1.1.1\n8.8.8.4")
	if err != nil {
		t.Error(err)
	}

	r.Mode = ModeRandom

	srv1, err := r.GetServer()
	if err != nil {
		t.Error(err)
	}
	srv2, err := r.GetServer()
	if err != nil {
		t.Error(err)
	}
	if srv1 == srv2 {
		t.Error(`two servers are the same, expecting different`)
	}
}

func TestGetServerRoundRobin(t *testing.T) {
	r := New()

	err := r.LoadFromString("8.8.8.8\n1.1.1.1\n8.8.8.4")
	if err != nil {
		t.Error(err)
	}

	r.Mode = ModeRoundRobin

	servers := r.GetServers()

	srv1, err := r.GetServer()
	if err != nil {
		t.Error(err)
	}
	if srv1 != servers[0] {
		t.Error(`the first server expected ` + servers[0].Addr)
	}

	srv2, err := r.GetServer()
	if err != nil {
		t.Error(err)
	}
	if srv2 != servers[1] {
		t.Error(`the second server expected ` + servers[1].Addr)
	}

	srv3, err := r.GetServer()
	if err != nil {
		t.Error(err)
	}
	if srv3 != servers[2] {
		t.Error(`the third server expected ` + servers[2].Addr)
	}

	srv4, err := r.GetServer()
	if err != nil {
		t.Error(err)
	}
	if srv4 != servers[0] {
		t.Error(`the fourth server expected ` + servers[0].Addr)
	}
}

func TestResolveHost(t *testing.T) {
	r := New()

	err := r.LoadFromString("8.8.8.8")
	if err != nil {
		t.Error(err)
	}

	ips, err := r.ResolveHost(`yandex.ru`)
	if err != nil {
		t.Error(err)
	}
	if len(ips) == 0 {
		t.Error(`ip list is empty`)
	}
}

func TestResolveNotExistsHost(t *testing.T) {
	r := New()

	err := r.LoadFromString("8.8.8.8")
	if err != nil {
		t.Error(err)
	}

	_, err = r.ResolveHost(`abc-123-def-456-zzzzzzz.com`)
	if err != ErrNoSuchHost {
		t.Error(err)
	}
}

func TestReverseIP(t *testing.T) {
	r := New()

	err := r.LoadFromString("8.8.8.8\n1.1.1.1\n8.8.8.4")
	if err != nil {
		t.Error(err)
	}

	hosts, err := r.ReverseIP(`5.255.255.70`)
	if err != nil {
		t.Error(err)
	}
	if len(hosts) == 0 {
		t.Error(`host list is empty`)
	}
}

func TestRetry(t *testing.T) {
	r := New()

	err := r.LoadFromString("1.0.0.0\n1.1.1.1\n2.0.0.0")
	if err != nil {
		t.Error(err)
	}

	ips, err := r.ResolveHost(`google.com`)
	if err != nil {
		t.Error(err)
	}
	if len(ips) == 0 {
		t.Error(`host list is empty`)
	}
}

func TestRetryLimit(t *testing.T) {
	r := New()
	r.RetryLimit = 2

	err := r.LoadFromString("1.0.0.0\n2.0.0.0\n3.0.0.0")
	if err != nil {
		t.Error(err)
	}

	_, err = r.ResolveHost(`google.com`)
	if err != ErrRetryLimit {
		t.Error(err)
	}
}

func TestRemoveBadServer(t *testing.T) {
	r := New()
	r.RetryLimit = 0
	r.MaxFails = 1

	err := r.LoadFromString("1.0.0.0")
	if err != nil {
		t.Error(err)
	}

	_, err = r.ResolveHost(`google.com`)
	if err != ErrServerListEmpty {
		t.Error(err)
	}
}

func TestBadServerList(t *testing.T) {
	r := New()

	err := r.LoadFromString("\n\nstring\n255.255.255.255\n127.0.0.1\n127.0.0.100\n0.0.0.0\n169.254.1.0\n169.254.254.255")
	if err != nil {
		t.Error(err)
	}

	if len(r.GetServers()) != 0 {
		t.Error(`expected empty server list`)
	}
}

func TestCache(t *testing.T) {
	r := New()
	r.CacheLimit = 2
	r.CacheLife = 1

	err := r.LoadFromString("8.8.8.8")
	if err != nil {
		t.Error(err)
	}

	_, err = r.ResolveHost(`google.com`)
	if err != nil {
		t.Error(err)
	}
	_, err = r.ResolveHost(`google.com`)
	if err != nil {
		t.Error(err)
	}
	_, err = r.ResolveHost(`yandex.com`)
	if err != nil {
		t.Error(err)
	}
	_, err = r.ResolveHost(`facebook.com`)
	if err != nil {
		t.Error(err)
	}
}

func TestConcurrency(t *testing.T) {
	r := New()
	r.CacheLimit = 50
	r.CacheLife = 10
	r.MaxFails = 1
	r.RetryLimit = 2

	err := r.LoadFromURL(NameServersURL)
	if err != nil {
		t.Error(err)
	}

	wg := sync.WaitGroup{}
	wg.Add(2)

	go func() {
		for i := 0; i < 1000; i++ {
			_, err := r.ResolveHost(`google.com`)
			if err != nil {
				t.Error(err)
				return
			}
		}
		wg.Done()
	}()

	go func() {
		for i := 0; i < 10; i++ {
			println(i)
			_, err := r.ResolveHost(fmt.Sprintf(`abc-%d`, i) + `.yandex.com`)
			if err != nil {
				t.Error(err)
				return
			}
		}
		wg.Done()
	}()

	wg.Wait()
}
