package config

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"github.com/nacos-group/nacos-sdk-go/v2/clients"
	"github.com/nacos-group/nacos-sdk-go/v2/clients/config_client"
	"github.com/nacos-group/nacos-sdk-go/v2/common/constant"
	"github.com/nacos-group/nacos-sdk-go/v2/vo"
)

type NacosProvider struct{}

func (NacosProvider) Load(ctx context.Context, bootstrap BootstrapConfig) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	client, err := newNacosConfigClient(bootstrap)
	if err != nil {
		return nil, err
	}
	defer client.CloseClient()
	content, err := client.GetConfig(vo.ConfigParam{DataId: bootstrap.Nacos.DataID, Group: bootstrap.Nacos.Group})
	if err != nil {
		return nil, fmt.Errorf("get Nacos dataId %q group %q: %w", bootstrap.Nacos.DataID, bootstrap.Nacos.Group, err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return []byte(content), nil
}

func (NacosProvider) Watch(ctx context.Context, bootstrap BootstrapConfig, onChange func([]byte)) (io.Closer, error) {
	if onChange == nil {
		return nil, errors.New("Nacos configuration change callback is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	client, err := newNacosConfigClient(bootstrap)
	if err != nil {
		return nil, err
	}
	watcher := &nacosConfigWatcher{client: client, done: make(chan struct{})}
	param := vo.ConfigParam{
		DataId: bootstrap.Nacos.DataID,
		Group:  bootstrap.Nacos.Group,
		OnChange: func(_, _, _, data string) {
			select {
			case <-watcher.done:
				return
			default:
			}
			onChange([]byte(data))
		},
	}
	watcher.param = param
	if err := client.ListenConfig(param); err != nil {
		client.CloseClient()
		return nil, fmt.Errorf("listen Nacos config %q/%q: %w", param.Group, param.DataId, err)
	}
	return watcher, nil
}

func newNacosConfigClient(bootstrap BootstrapConfig) (config_client.IConfigClient, error) {
	servers, err := parseNacosServers(bootstrap.Nacos.ServerAddresses)
	if err != nil {
		return nil, err
	}
	clientConfig := constant.ClientConfig{
		NamespaceId:         bootstrap.Nacos.Namespace,
		TimeoutMs:           uint64(bootstrap.Nacos.Timeout.Milliseconds()),
		NotLoadCacheAtStart: true,
		CacheDir:            bootstrap.Nacos.CacheDir,
		LogDir:              bootstrap.Nacos.LogDir,
		LogLevel:            bootstrap.Nacos.LogLevel,
		Username:            bootstrap.Nacos.Username,
		Password:            bootstrap.Nacos.Password,
	}
	return clients.NewConfigClient(vo.NacosClientParam{ClientConfig: &clientConfig, ServerConfigs: servers})
}

type nacosConfigWatcher struct {
	client config_client.IConfigClient
	param  vo.ConfigParam
	done   chan struct{}
	once   sync.Once
}

func (watcher *nacosConfigWatcher) Close() error {
	if watcher == nil || watcher.client == nil {
		return nil
	}
	var err error
	watcher.once.Do(func() {
		close(watcher.done)
		err = watcher.client.CancelListenConfig(watcher.param)
		watcher.client.CloseClient()
	})
	return err
}

func parseNacosServers(value string) ([]constant.ServerConfig, error) {
	parts := strings.Split(value, ",")
	servers := make([]constant.ServerConfig, 0, len(parts))
	for _, part := range parts {
		address := strings.TrimSpace(part)
		if address == "" {
			continue
		}
		if !strings.Contains(address, "://") {
			address = "http://" + address
		}
		parsed, err := url.Parse(address)
		if err != nil || parsed.Hostname() == "" {
			return nil, fmt.Errorf("invalid Nacos server address %q", part)
		}
		if parsed.Scheme != "http" && parsed.Scheme != "https" {
			return nil, fmt.Errorf("Nacos server address %q must use http or https", part)
		}
		port := uint64(8848)
		if parsed.Port() != "" {
			parsedPort, err := strconv.ParseUint(parsed.Port(), 10, 16)
			if err != nil {
				return nil, fmt.Errorf("invalid Nacos port in %q", part)
			}
			port = parsedPort
		} else if strings.Contains(parsed.Host, ":") && net.ParseIP(parsed.Host) == nil {
			if _, _, err := net.SplitHostPort(parsed.Host); err != nil {
				return nil, fmt.Errorf("invalid Nacos server address %q", part)
			}
		}
		contextPath := parsed.Path
		if contextPath == "" {
			contextPath = "/nacos"
		}
		servers = append(servers, constant.ServerConfig{Scheme: parsed.Scheme, ContextPath: contextPath, IpAddr: parsed.Hostname(), Port: port})
	}
	if len(servers) == 0 {
		return nil, errors.New("at least one Nacos server address is required")
	}
	return servers, nil
}
