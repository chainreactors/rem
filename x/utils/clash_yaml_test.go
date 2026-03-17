package utils

import (
	"strings"
	"testing"
)

func TestMarshalClashConfigYAML(t *testing.T) {
	config := &ClashConfig{
		Proxies: []*Proxies{
			{
				Name:           "node-1",
				Type:           "socks5",
				Server:         "127.0.0.1",
				Port:           1080,
				Username:       "user",
				Password:       "pass",
				Udp:            true,
				Tls:            false,
				SkipCertVerify: true,
				Cipher:         "none",
			},
		},
		Mode: "rule",
		Rules: []string{
			"IP-CIDR,10.0.0.0/8,10_NET",
			"MATCH,DIRECT",
		},
		ProxyGroups: []*ProxyGroup{
			{
				Name:    "10_NET",
				Type:    "select",
				Proxies: []string{"node-1", "DIRECT"},
			},
		},
	}

	data, err := MarshalClashConfigYAML(config)
	if err != nil {
		t.Fatalf("MarshalClashConfigYAML failed: %v", err)
	}

	text := string(data)
	checks := []string{
		"proxies:\n",
		"- name: \"node-1\"\n",
		"mode: \"rule\"\n",
		"rules:\n",
		"- \"MATCH,DIRECT\"\n",
		"proxy-groups:\n",
		"proxies: [\"node-1\", \"DIRECT\"]\n",
	}

	for _, check := range checks {
		if !strings.Contains(text, check) {
			t.Fatalf("generated yaml missing %q\n%s", check, text)
		}
	}
}

func TestMarshalClashConfigYAMLEmpty(t *testing.T) {
	config := &ClashConfig{}
	data, err := MarshalClashConfigYAML(config)
	if err != nil {
		t.Fatalf("MarshalClashConfigYAML failed: %v", err)
	}

	text := string(data)
	checks := []string{
		"proxies: []\n",
		"mode: \"\"\n",
		"rules: []\n",
		"proxy-groups: []\n",
	}

	for _, check := range checks {
		if !strings.Contains(text, check) {
			t.Fatalf("generated yaml missing %q\n%s", check, text)
		}
	}
}
