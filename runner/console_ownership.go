package runner

import (
	"fmt"

	"github.com/chainreactors/rem/agent"
)

// Agent returns only agents whose root connection belongs to this console.
// Registry names alone are never an ownership credential.
func (c *Console) Agent(id string) (*agent.Agent, bool) {
	a, ok := agent.Agents.Get(id)
	if !ok {
		return nil, false
	}
	return a, c.owns(a) && !a.IsClosed()
}

func (c *Console) owns(a *agent.Agent) bool {
	c.ownerMu.Lock()
	defer c.ownerMu.Unlock()
	root := c.owned[a.Root().ID]
	return !c.closed && root == a.Root()
}

func (c *Console) Agents() map[string]*agent.Agent {
	result := make(map[string]*agent.Agent)
	agent.Agents.Range(func(key, value interface{}) bool {
		a := value.(*agent.Agent)
		if owned, ok := c.Agent(a.ID); ok && owned == a {
			result[a.ID] = a
		}
		return true
	})
	return result
}

func (c *Console) registerAgent(a *agent.Agent) error {
	c.ownerMu.Lock()
	defer c.ownerMu.Unlock()
	if c.closed {
		return fmt.Errorf("console is closed")
	}
	root := a.Root()
	if a.IsClosed() || root.IsClosed() {
		return fmt.Errorf("agent connection is closed")
	}
	// A delayed fork may finish after its root connection was replaced. Only
	// root registration can establish ownership of a new connection generation.
	if a != root {
		current, ok := agent.Agents.Get(root.ID)
		if c.owned[root.ID] != root || !ok || current != root {
			return fmt.Errorf("agent root connection no longer belongs to this console")
		}
	}
	if err := agent.Agents.Add(a); err != nil {
		return err
	}
	if c.owned == nil {
		c.owned = make(map[string]*agent.Agent)
	}
	if a == root {
		c.owned[root.ID] = root
	}
	return nil
}

func (c *Console) isClosed() bool {
	c.ownerMu.Lock()
	defer c.ownerMu.Unlock()
	return c.closed
}
