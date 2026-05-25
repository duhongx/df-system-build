package deployer

import "sort"

// registry holds all registered components
var registry []Component

// Register adds a component to the registry
func Register(c Component) {
	registry = append(registry, c)
}

// GetComponents returns all registered components sorted by order
func GetComponents() []Component {
	sorted := make([]Component, len(registry))
	copy(sorted, registry)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Order() < sorted[j].Order()
	})
	return sorted
}

// GetComponent returns a component by code
func GetComponent(code string) Component {
	for _, c := range registry {
		if c.Code() == code {
			return c
		}
	}
	return nil
}
