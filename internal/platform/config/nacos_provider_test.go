package config

import "testing"

func TestParseNacosServers(t *testing.T) {
	servers, err := parseNacosServers("http://127.0.0.1:8848,https://nacos.example.com:443/custom")
	if err != nil {
		t.Fatal(err)
	}
	if len(servers) != 2 || servers[0].IpAddr != "127.0.0.1" || servers[0].Port != 8848 || servers[0].ContextPath != "/nacos" {
		t.Fatalf("unexpected Nacos servers: %#v", servers)
	}
	if servers[1].Scheme != "https" || servers[1].ContextPath != "/custom" || servers[1].Port != 443 {
		t.Fatalf("unexpected custom Nacos server: %#v", servers[1])
	}
}

func TestParseNacosServersRejectsInvalidAddress(t *testing.T) {
	if _, err := parseNacosServers("ftp://nacos.example.com:8848"); err == nil {
		t.Fatal("expected invalid Nacos scheme error")
	}
	if _, err := parseNacosServers(" "); err == nil {
		t.Fatal("expected empty Nacos server error")
	}
}
