package llama

import (
	"regexp"
	"sort"
)

// levelStrength orders the levels from least thinking to most, and is also the
// set of names the harness will send. A template may name others; those are
// dropped rather than offered.
var levelStrength = map[Effort]int{
	EffortMinimal: 1,
	EffortLow:     2,
	EffortMedium:  3,
	EffortHigh:    4,
	EffortXHigh:   5,
	EffortMax:     6,
}

// fallbackLevels is what a picker offers when the template named no set. The
// three are also the three thinking budgets, so they mean something either way.
var fallbackLevels = []Effort{EffortLow, EffortMedium, EffortHigh}

// A template that checks the effort it was given lists the names it takes in a
// "not in" test and raises on anything else. That test is the only place the
// accepted set is written down.
var effortSetTest = regexp.MustCompile(`reasoning_(?:effort|strength)[^%}]{0,40}?not\s+in\s*[\(\[]([^)\]]*)[\)\]]`)

var quotedName = regexp.MustCompile(`'([^']*)'|"([^"]*)"`)

// templateEffortLevels reads the levels a chat template accepts out of its
// source, weakest first. Nil means the template named no set, which is not the
// same as accepting none: a template without the test passes any name through.
func templateEffortLevels(template string) []Effort {
	m := effortSetTest.FindStringSubmatch(template)
	if m == nil {
		return nil
	}
	var out []Effort
	seen := map[Effort]bool{}
	for _, q := range quotedName.FindAllStringSubmatch(m[1], -1) {
		name := Effort(q[1])
		if name == "" {
			name = Effort(q[2])
		}
		if _, known := levelStrength[name]; !known || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	sort.Slice(out, func(i, j int) bool { return levelStrength[out[i]] < levelStrength[out[j]] })
	return out
}

// EffortLevels is what a picker should offer between auto and off.
func EffortLevels(caps Caps) []Effort {
	if len(caps.EffortLevels) > 0 {
		return caps.EffortLevels
	}
	return fallbackLevels
}

// accepts reports whether the template will take this level. A template that
// named no set takes anything, so only a named set can refuse.
func (c Caps) accepts(e Effort) bool {
	if len(c.EffortLevels) == 0 {
		return true
	}
	for _, l := range c.EffortLevels {
		if l == e {
			return true
		}
	}
	return false
}
