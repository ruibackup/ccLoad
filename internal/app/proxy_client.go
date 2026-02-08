package app

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	"ccLoad/internal/config"

	"golang.org/x/net/proxy"
)

// proxyClientPool 按 proxy_url 缓存 HTTP Client，避免重复创建
type proxyClientPool struct {
	clients        sync.Map // key=proxyURL(string), value=*http.Client
	skipTLSVerify  bool
}

// newProxyClientPool 创建代理客户端池
func newProxyClientPool(skipTLSVerify bool) *proxyClientPool {
	return &proxyClientPool{
		skipTLSVerify: skipTLSVerify,
	}
}

// GetClient 获取指定代理URL的HTTP Client
// 空URL返回nil（调用方应使用默认client）
func (p *proxyClientPool) GetClient(proxyURL string) (*http.Client, error) {
	if proxyURL == "" {
		return nil, nil
	}

	// 从缓存获取
	if v, ok := p.clients.Load(proxyURL); ok {
		return v.(*http.Client), nil
	}

	// 创建新的 transport
	transport, err := p.buildTransport(proxyURL)
	if err != nil {
		return nil, err
	}

	client := &http.Client{
		Transport: transport,
		Timeout:   0, // 不设置全局超时，与默认client一致
	}

	// 存入缓存（LoadOrStore 保证并发安全，多个goroutine同时创建时只保留一个）
	actual, _ := p.clients.LoadOrStore(proxyURL, client)
	return actual.(*http.Client), nil
}

// buildTransport 根据代理URL构建 http.Transport
func (p *proxyClientPool) buildTransport(proxyURL string) (*http.Transport, error) {
	u, err := url.Parse(proxyURL)
	if err != nil {
		return nil, fmt.Errorf("parse proxy url: %w", err)
	}

	tlsConfig := &tls.Config{
		ClientSessionCache: tls.NewLRUClientSessionCache(config.TLSSessionCacheSize),
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: p.skipTLSVerify, //nolint:gosec // G402: 由环境变量控制
	}

	switch u.Scheme {
	case "http", "https":
		// HTTP/HTTPS 代理：使用 http.ProxyURL
		proxyFunc := http.ProxyURL(u)
		return &http.Transport{
			Proxy:                proxyFunc,
			MaxIdleConns:         config.HTTPMaxIdleConns,
			MaxIdleConnsPerHost:  config.HTTPMaxIdleConnsPerHost,
			IdleConnTimeout:      90 * time.Second,
			MaxConnsPerHost:      config.HTTPMaxConnsPerHost,
			TLSHandshakeTimeout:  config.HTTPTLSHandshakeTimeout,
			ForceAttemptHTTP2:    true,
			TLSClientConfig:     tlsConfig,
		}, nil

	case "socks5", "socks5h":
		// SOCKS5 代理：使用 golang.org/x/net/proxy
		auth := &proxy.Auth{}
		hasAuth := false
		if u.User != nil {
			auth.User = u.User.Username()
			auth.Password, _ = u.User.Password()
			hasAuth = true
		}

		var dialer proxy.Dialer
		if hasAuth {
			dialer, err = proxy.SOCKS5("tcp", u.Host, auth, proxy.Direct)
		} else {
			dialer, err = proxy.SOCKS5("tcp", u.Host, nil, proxy.Direct)
		}
		if err != nil {
			return nil, fmt.Errorf("create socks5 dialer: %w", err)
		}

		// proxy.Dialer 实现了 DialContext（通过 proxy.ContextDialer 接口）
		contextDialer, ok := dialer.(proxy.ContextDialer)
		if !ok {
			return nil, fmt.Errorf("socks5 dialer does not support DialContext")
		}

		return &http.Transport{
			DialContext:          contextDialer.DialContext,
			MaxIdleConns:         config.HTTPMaxIdleConns,
			MaxIdleConnsPerHost:  config.HTTPMaxIdleConnsPerHost,
			IdleConnTimeout:      90 * time.Second,
			MaxConnsPerHost:      config.HTTPMaxConnsPerHost,
			TLSHandshakeTimeout:  config.HTTPTLSHandshakeTimeout,
			ForceAttemptHTTP2:    true,
			TLSClientConfig:     tlsConfig,
		}, nil

	default:
		return nil, fmt.Errorf("unsupported proxy scheme: %q", u.Scheme)
	}
}

// compile-time check: net.Dialer implements proxy.Direct's interface
var _ net.Dialer
