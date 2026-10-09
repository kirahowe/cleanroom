package core

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Labels says what a session or a stored value carries. The zero value is a
// clean session: it has read nothing untrusted and nothing private.
//
// Labels only ever grow. Nothing in cleanroom removes one.
type Labels struct {
	// Contaminated means untrusted content is present, so whatever holds
	// it may be acting on someone else's instructions.
	Contaminated bool
	// Private means the user's private data is present, so there is
	// something to leak.
	Private bool
}

// Union returns the labels of something that carries both l and m.
func (l Labels) Union(m Labels) Labels {
	return Labels{
		Contaminated: l.Contaminated || m.Contaminated,
		Private:      l.Private || m.Private,
	}
}

// Contains reports whether l has every label that m has.
func (l Labels) Contains(m Labels) bool { return l.Union(m) == l }

func (l Labels) names() []string {
	names := []string{}
	if l.Contaminated {
		names = append(names, "contaminated")
	}
	if l.Private {
		names = append(names, "private")
	}
	return names
}

func (l Labels) String() string {
	if l == (Labels{}) {
		return "clean"
	}
	return strings.Join(l.names(), ", ")
}

// MarshalJSON writes labels as a list of names, for example
// ["contaminated","private"]. The policy file and the log both use this form.
func (l Labels) MarshalJSON() ([]byte, error) { return json.Marshal(l.names()) }

// UnmarshalJSON reads the list form. An unknown name is an error, so a typo
// in a policy file cannot silently drop a label.
func (l *Labels) UnmarshalJSON(raw []byte) error {
	var names []string
	if err := json.Unmarshal(raw, &names); err != nil {
		return fmt.Errorf("labels must be a list of names: %w", err)
	}
	*l = Labels{}
	for _, n := range names {
		switch n {
		case "contaminated":
			l.Contaminated = true
		case "private":
			l.Private = true
		default:
			return fmt.Errorf("unknown label %q", n)
		}
	}
	return nil
}
