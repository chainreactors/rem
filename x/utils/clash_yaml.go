package utils

import (
	"errors"
	"strconv"
	"strings"
)

func MarshalClashConfigYAML(config *ClashConfig) ([]byte, error) {
	if config == nil {
		return nil, errors.New("clash config is nil")
	}

	var builder strings.Builder
	writeProxyList(&builder, config.Proxies)
	writeStringValue(&builder, "mode", config.Mode)
	writeStringList(&builder, "rules", config.Rules)
	writeProxyGroupList(&builder, config.ProxyGroups)

	return []byte(builder.String()), nil
}

func writeProxyList(builder *strings.Builder, proxies []*Proxies) {
	if len(proxies) == 0 {
		builder.WriteString("proxies: []\n")
		return
	}

	builder.WriteString("proxies:\n")
	for _, proxy := range proxies {
		if proxy == nil {
			builder.WriteString("  - null\n")
			continue
		}

		first := true
		if proxy.Name != "" {
			writeObjectField(builder, &first, "name", quoteYAMLString(proxy.Name))
		}
		writeObjectField(builder, &first, "type", quoteYAMLString(proxy.Type))
		writeObjectField(builder, &first, "server", quoteYAMLString(proxy.Server))
		writeObjectField(builder, &first, "port", strconv.Itoa(proxy.Port))
		if proxy.Username != "" {
			writeObjectField(builder, &first, "username", quoteYAMLString(proxy.Username))
		}
		if proxy.Password != "" {
			writeObjectField(builder, &first, "password", quoteYAMLString(proxy.Password))
		}
		writeObjectField(builder, &first, "udp", strconv.FormatBool(proxy.Udp))
		writeObjectField(builder, &first, "tls", strconv.FormatBool(proxy.Tls))
		writeObjectField(builder, &first, "skip-cert-verify", strconv.FormatBool(proxy.SkipCertVerify))
		if proxy.Cipher != "" {
			writeObjectField(builder, &first, "cipher", quoteYAMLString(proxy.Cipher))
		}
		if first {
			builder.WriteString("  - {}\n")
		}
	}
}

func writeProxyGroupList(builder *strings.Builder, groups []*ProxyGroup) {
	if len(groups) == 0 {
		builder.WriteString("proxy-groups: []\n")
		return
	}

	builder.WriteString("proxy-groups:\n")
	for _, group := range groups {
		if group == nil {
			builder.WriteString("  - null\n")
			continue
		}

		first := true
		writeObjectField(builder, &first, "name", quoteYAMLString(group.Name))
		writeObjectField(builder, &first, "type", quoteYAMLString(group.Type))
		writeObjectField(builder, &first, "proxies", quoteYAMLInlineStringSlice(group.Proxies))
		if first {
			builder.WriteString("  - {}\n")
		}
	}
}

func writeStringValue(builder *strings.Builder, key, value string) {
	builder.WriteString(key)
	builder.WriteString(": ")
	builder.WriteString(quoteYAMLString(value))
	builder.WriteByte('\n')
}

func writeStringList(builder *strings.Builder, key string, values []string) {
	if len(values) == 0 {
		builder.WriteString(key)
		builder.WriteString(": []\n")
		return
	}

	builder.WriteString(key)
	builder.WriteString(":\n")
	for _, value := range values {
		builder.WriteString("  - ")
		builder.WriteString(quoteYAMLString(value))
		builder.WriteByte('\n')
	}
}

func writeObjectField(builder *strings.Builder, first *bool, key, value string) {
	if *first {
		builder.WriteString("  - ")
		*first = false
	} else {
		builder.WriteString("    ")
	}
	builder.WriteString(key)
	builder.WriteString(": ")
	builder.WriteString(value)
	builder.WriteByte('\n')
}

func quoteYAMLString(value string) string {
	return strconv.Quote(value)
}

func quoteYAMLInlineStringSlice(values []string) string {
	if len(values) == 0 {
		return "[]"
	}

	quotedValues := make([]string, 0, len(values))
	for _, value := range values {
		quotedValues = append(quotedValues, quoteYAMLString(value))
	}

	return "[" + strings.Join(quotedValues, ", ") + "]"
}
