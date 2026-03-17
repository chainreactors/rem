package runner

import (
	"fmt"
	"sort"
	"strings"

	"github.com/chainreactors/rem/protocol/core"
)

// ListRegistered 列出所有已注册的组件
func ListRegistered() {
	fmt.Println("=== Registered Components ===")
	fmt.Println()

	// Tunnels
	fmt.Println("Tunnels (Dialers):")
	dialers := core.GetRegisteredDialers()
	sort.Strings(dialers)
	if len(dialers) > 0 {
		for _, name := range dialers {
			fmt.Printf("  - %s\n", name)
		}
	} else {
		fmt.Println("  (none)")
	}
	fmt.Println()

	fmt.Println("Tunnels (Listeners):")
	listeners := core.GetRegisteredListeners()
	sort.Strings(listeners)
	if len(listeners) > 0 {
		for _, name := range listeners {
			fmt.Printf("  - %s\n", name)
		}
	} else {
		fmt.Println("  (none)")
	}
	fmt.Println()

	// Services
	fmt.Println("Services (Inbound):")
	inbounds := core.GetRegisteredInbounds()
	sort.Strings(inbounds)
	if len(inbounds) > 0 {
		for _, name := range inbounds {
			fmt.Printf("  - %s\n", name)
		}
	} else {
		fmt.Println("  (none)")
	}
	fmt.Println()

	fmt.Println("Services (Outbound):")
	outbounds := core.GetRegisteredOutbounds()
	sort.Strings(outbounds)
	if len(outbounds) > 0 {
		for _, name := range outbounds {
			fmt.Printf("  - %s\n", name)
		}
	} else {
		fmt.Println("  (none)")
	}
	fmt.Println()

	// Wrappers
	fmt.Println("Wrappers:")
	wrappers := core.GetRegisteredWrappers()
	sort.Strings(wrappers)
	if len(wrappers) > 0 {
		for _, name := range wrappers {
			fmt.Printf("  - %s\n", name)
		}
	} else {
		fmt.Println("  (none)")
	}
	fmt.Println()
}

// GetAvailableComponents 获取所有可用组件的字符串表示（用于错误提示）
func GetAvailableComponents(componentType string) string {
	var components []string
	switch strings.ToLower(componentType) {
	case "dialer":
		components = core.GetRegisteredDialers()
	case "listener":
		components = core.GetRegisteredListeners()
	case "inbound":
		components = core.GetRegisteredInbounds()
	case "outbound":
		components = core.GetRegisteredOutbounds()
	case "wrapper":
		components = core.GetRegisteredWrappers()
	default:
		return ""
	}

	if len(components) == 0 {
		return "(none registered)"
	}

	sort.Strings(components)
	return strings.Join(components, ", ")
}
