package geecache

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
)

// LoadPeerServerTLS 构造供「仅节点」监听器使用的 *tls.Config：服务端出示 cert/key，
// 并要求对端出示由 clientCAFile 所代表 CA 签发的客户端证书。
func LoadPeerServerTLS(certFile, keyFile, clientCAFile string) (*tls.Config, error) {
	if certFile == "" || keyFile == "" || clientCAFile == "" {
		return nil, fmt.Errorf("geecache: peer server TLS: cert, key, and client CA paths are required")
	}
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("geecache: load server key pair: %w", err)
	}
	caPEM, err := os.ReadFile(clientCAFile)
	if err != nil {
		return nil, fmt.Errorf("geecache: read client CA: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("geecache: client CA PEM has no certificates")
	}
	cfg := &tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    pool,
		MinVersion:   tls.VersionTLS12,
		NextProtos:   []string{"h2", "http/1.1"},
	}
	return cfg, nil
}

// LoadPeerClientTLS 构造访问其它节点时的 *tls.Config：用 serverCAFile 校验对端服务证书，
// 并出示 clientCertFile / clientKeyFile 作为本机客户端身份。
func LoadPeerClientTLS(serverCAFile, clientCertFile, clientKeyFile string) (*tls.Config, error) {
	if serverCAFile == "" || clientCertFile == "" || clientKeyFile == "" {
		return nil, fmt.Errorf("geecache: peer client TLS: server CA, client cert, and client key paths are required")
	}
	caPEM, err := os.ReadFile(serverCAFile)
	if err != nil {
		return nil, fmt.Errorf("geecache: read server CA: %w", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("geecache: server CA PEM has no certificates")
	}
	cert, err := tls.LoadX509KeyPair(clientCertFile, clientKeyFile)
	if err != nil {
		return nil, fmt.Errorf("geecache: load client key pair: %w", err)
	}
	cfg := &tls.Config{
		RootCAs:      roots,
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	}
	return cfg, nil
}
