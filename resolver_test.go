package resolver

import (
	"fmt"
	"github.com/zofan/go-slist"
	"sync"
	"testing"
)

func TestResolveHost(t *testing.T) {
	r := New()

	err := r.Servers.LoadFromString("8.8.8.8")
	if err != nil {
		t.Error(err)
	}

	ips, err := r.LookupIPAddr(`yandex.ru`)
	if err != nil {
		t.Error(err)
	}
	if len(ips) == 0 {
		t.Error(`ip list is empty`)
	}
}

func TestResolveNotExistsHost(t *testing.T) {
	r := New()

	err := r.Servers.LoadFromString("8.8.8.8")
	if err != nil {
		t.Error(err)
	}

	_, err = r.LookupIPAddr(`abc-123-def-456-zzzzzzz.com`)
	if err != ErrNoSuchHost {
		t.Error(err)
	}
}

func TestReverseIP(t *testing.T) {
	r := New()

	err := r.Servers.LoadFromString("8.8.8.8\n1.1.1.1\n8.8.8.4")
	if err != nil {
		t.Error(err)
	}

	hosts, err := r.LookupAddr(`5.255.255.70`)
	if err != nil {
		t.Error(err)
	}
	if len(hosts) == 0 {
		t.Error(`host list is empty`)
	}
}

func TestRetry(t *testing.T) {
	r := New()

	err := r.Servers.LoadFromString("1.0.0.0\n1.1.1.1\n2.0.0.0")
	if err != nil {
		t.Error(err)
	}

	ips, err := r.LookupIPAddr(`google.com`)
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

	err := r.Servers.LoadFromString("1.0.0.0\n2.0.0.0\n3.0.0.0")
	if err != nil {
		t.Error(err)
	}

	_, err = r.LookupIPAddr(`google.com`)
	if err != ErrRetryLimit {
		t.Error(err)
	}
}

func TestRemoveBadServer(t *testing.T) {
	r := New()
	r.RetryLimit = 0
	r.MaxFails = 1

	err := r.Servers.LoadFromString("1.0.0.0")
	if err != nil {
		t.Error(err)
	}

	_, err = r.LookupIPAddr(`google.com`)
	if err != slist.ErrServerListEmpty {
		t.Error(err)
	}
}

func TestConcurrency(t *testing.T) {
	r := New()
	r.CacheLimit = 50
	r.CacheLife = 10
	r.MaxFails = 1
	r.RetryLimit = 2

	err := r.Servers.LoadFromURL(ServerListURL)
	if err != nil {
		t.Error(err)
	}

	wg := sync.WaitGroup{}
	wg.Add(2)

	go func() {
		for i := 0; i < 1000; i++ {
			_, err := r.LookupIPAddr(`google.com`)
			if err != nil {
				t.Error(err)
				return
			}
		}
		wg.Done()
	}()

	go func() {
		for i := 0; i < 10; i++ {
			_, err := r.LookupIPAddr(fmt.Sprintf(`abc-%d`, i) + `-yandex.com`)
			if err == nil {
				t.Error(err)
			}
		}
		wg.Done()
	}()

	wg.Wait()
}
