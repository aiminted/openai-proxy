package pricing

import (
	"fmt"
	"os"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

// Prices are USD per 1M tokens.
type ModelPrice struct {
	Input  float64 `yaml:"input"`
	Cached float64 `yaml:"cached,omitempty"`
	Output float64 `yaml:"output"`
}

type Table struct {
	Models map[string]ModelPrice `yaml:"models"`
}

type Pricing struct {
	mu     sync.RWMutex
	models map[string]ModelPrice
}

func Load(path string) (*Pricing, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read pricing %s: %w", path, err)
	}
	var t Table
	if err := yaml.Unmarshal(data, &t); err != nil {
		return nil, fmt.Errorf("parse pricing: %w", err)
	}
	if t.Models == nil {
		t.Models = map[string]ModelPrice{}
	}
	return &Pricing{models: t.Models}, nil
}

// Empty returns a Pricing with no models configured. Cost will always be 0.
func Empty() *Pricing {
	return &Pricing{models: map[string]ModelPrice{}}
}

// Cost returns the dollar cost of a request. Models not in the table cost 0.
// Cached tokens are billed at the cached rate; if no cached rate is set, they
// are billed at the regular input rate.
func (p *Pricing) Cost(model string, input, output, cached int) float64 {
	p.mu.RLock()
	defer p.mu.RUnlock()
	mp, ok := p.lookup(model)
	if !ok {
		return 0
	}
	cachedRate := mp.Cached
	if cachedRate == 0 {
		cachedRate = mp.Input
	}
	uncached := input - cached
	if uncached < 0 {
		cached, uncached = input, 0
	}
	return (float64(uncached)*mp.Input + float64(cached)*cachedRate + float64(output)*mp.Output) / 1_000_000
}

// lookup tries the exact model id, then progressively shorter prefixes
// (gpt-5.5-2026-04-24 -> gpt-5.5).
func (p *Pricing) lookup(model string) (ModelPrice, bool) {
	if mp, ok := p.models[model]; ok {
		return mp, true
	}
	for i := strings.LastIndex(model, "-"); i > 0; i = strings.LastIndex(model[:i], "-") {
		if mp, ok := p.models[model[:i]]; ok {
			return mp, true
		}
	}
	return ModelPrice{}, false
}
