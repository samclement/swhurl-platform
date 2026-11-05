package kernel

import (
	"encoding/json"
	"regexp"
	"slices"
	"strings"
)

// The names catalogue (contract/names.json) lists every name the platform provides to apps: the variables it
// sets, the labels and annotations it writes, and the overrides a reader may take. An entry whose name holds one
// <PART> is a family: the part stands for any upper-case name, so SWHURL_SERVICE_<APP>_URL lists
// SWHURL_SERVICE_ORDERS_URL, and the family's prefix alone (SWHURL_SERVICE_) counts as listed because a reader
// builds the rest at run time.
const namesKind = "platform-names"

// platformName is a platform name anywhere in a file: an app reads it, or the platform writes it.
var platformName = regexp.MustCompile(`SWHURL_[A-Z0-9_]+|platform\.swhurl\.com/[a-z0-9-]+`)

// NameCatalogue is the decoded catalogue: the names it lists and its families.
type NameCatalogue struct {
	listed   map[string]bool
	families []nameFamily
}

// nameFamily is one family entry's text before and after its <PART>.
type nameFamily struct{ prefix, suffix string }

// nameEntryFields are an entry's fields: each is a non-empty string.
var nameEntryFields = []string{"name", "surface", "meaning", "availability"}

// DecodeNameCatalogue reads the catalogue. The document is strict: unknown fields, nulls, a missing or empty field, a
// repeated name and a name with more than one <PART> are refused.
func DecodeNameCatalogue(data []byte) (NameCatalogue, error) {
	if err := document(data); err != nil {
		return NameCatalogue{}, err
	}
	root, err := fields(data, "names", []string{"kind", "version", "names"})
	if err != nil {
		return NameCatalogue{}, err
	}
	var kind string
	var version int
	var entries []json.RawMessage
	if json.Unmarshal(root["kind"], &kind) != nil || json.Unmarshal(root["version"], &version) != nil ||
		json.Unmarshal(root["names"], &entries) != nil {
		return NameCatalogue{}, refuse(Invalid, "names", "a field has the wrong type")
	}
	if kind != namesKind || version != 1 {
		return NameCatalogue{}, refuse(Unsupported, "kind", "only platform-names version 1 is supported")
	}
	catalogue := NameCatalogue{listed: map[string]bool{}}
	for _, entry := range entries {
		if err := catalogue.add(entry); err != nil {
			return NameCatalogue{}, err
		}
	}
	return catalogue, nil
}

// add lists one entry, as a name or as a family.
func (c *NameCatalogue) add(data json.RawMessage) error {
	entry, err := fields(data, "names", nameEntryFields)
	if err != nil {
		return err
	}
	values := map[string]string{}
	for _, key := range nameEntryFields {
		var value string
		if json.Unmarshal(entry[key], &value) != nil || value == "" {
			return refuse(Invalid, "names."+key, "every entry states a name, surface, meaning and availability")
		}
		values[key] = value
	}
	name := values["name"]
	if c.listed[name] {
		return refuse(Invalid, "names", "a name is listed twice")
	}
	c.listed[name] = true
	prefix, rest, isFamily := strings.Cut(name, "<")
	if !isFamily {
		return nil
	}
	_, suffix, closed := strings.Cut(rest, ">")
	if !closed || prefix == "" || strings.ContainsAny(suffix, "<>") {
		return refuse(Invalid, "names", "a family name holds exactly one <PART> after a prefix")
	}
	c.families = append(c.families, nameFamily{prefix, suffix})
	return nil
}

// Unlisted is each platform name in text that the catalogue does not list, once each, in order of first use.
func (c NameCatalogue) Unlisted(text []byte) []string {
	var found []string
	for _, match := range platformName.FindAll(text, -1) {
		name := string(match)
		if !c.lists(name) && !slices.Contains(found, name) {
			found = append(found, name)
		}
	}
	return found
}

// lists reports whether name is listed, a member of a family, or a family's prefix alone.
func (c NameCatalogue) lists(name string) bool {
	if c.listed[name] {
		return true
	}
	for _, family := range c.families {
		if name == family.prefix {
			return true
		}
		middle, ok := strings.CutPrefix(name, family.prefix)
		if !ok {
			continue
		}
		middle, ok = strings.CutSuffix(middle, family.suffix)
		if ok && middle != "" && strings.Trim(middle, "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_") == "" {
			return true
		}
	}
	return false
}
