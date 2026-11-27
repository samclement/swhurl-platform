package apps

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/samclement/swhurl-platform/v2/kernel"
)

// Setting is one named change as `name=value` states it: the setting's name, and the kernel rule bound to the value.
// Team is the one app-level setting: it names the app's new team and reaches every environment, so it has no rule
// of its own.
type Setting struct {
	Name  string
	Team  string
	Apply func(kernel.InstanceSpec, kernel.ReleaseBundle) (kernel.InstanceSpec, error)
}

// AppLevel is whether the setting is the app's rather than one instance's.
func (s Setting) AppLevel() bool { return s.Team != "" }

// settings is every setting an edit names, with the parser that reads its value. A setting v2 does not model is named
// too, so it is refused by name instead of being mistaken for another fact: an alert is a platform document it does
// not render yet.
var settings = map[string]func(string) (Setting, error){
	"replicas":      instanceSetting("replicas", kernel.WithReplicas),
	"memoryMiB":     instanceSetting("memoryMiB", kernel.WithMemory),
	"exposure":      releaseSetting("exposure", kernel.WithExposure),
	"calls":         listSetting("calls", kernel.WithCalls),
	"allows":        listSetting("allows", kernel.WithAllows),
	"subscriptions": listSetting("subscriptions", kernel.WithSubscriptions),
	"team":          teamSetting,
	"egress":        listSetting("egress", kernel.WithEgress),
	"alerts":        notModelled("alerts"),
}

// ParseSetting reads `name=value` as the kernel rule it names.
func ParseSetting(setting string) (Setting, error) {
	name, value, found := strings.Cut(setting, "=")
	if !found {
		return Setting{}, fmt.Errorf("invalid: set: expected name=value")
	}
	build, known := settings[name]
	if !known {
		return Setting{}, fmt.Errorf("invalid: set: unknown setting %q", name)
	}
	return build(value)
}

// instanceSetting is a change whose rule reads only the record.
func instanceSetting(name string, rule func(kernel.InstanceSpec, int32) (kernel.InstanceSpec, error)) func(string) (Setting, error) {
	return func(value string) (Setting, error) {
		count, err := settingInteger(name, value)
		if err != nil {
			return Setting{}, err
		}
		return Setting{Name: name,
			Apply: func(spec kernel.InstanceSpec, _ kernel.ReleaseBundle) (kernel.InstanceSpec, error) {
				return rule(spec, count)
			}}, nil
	}
}

// releaseSetting is a change the release has a say in: the kind decides whether the setting is meaningful at all.
func releaseSetting(name string, rule func(kernel.InstanceSpec, kernel.ReleaseBundle, string) (kernel.InstanceSpec, error)) func(string) (Setting, error) {
	return func(value string) (Setting, error) {
		return Setting{Name: name, Apply: func(spec kernel.InstanceSpec, release kernel.ReleaseBundle) (kernel.InstanceSpec, error) {
			return rule(spec, release, value)
		}}, nil
	}
}

// listSetting is a change whose value is a declaration list: names separated by commas, and an empty value clears it.
func listSetting(name string, rule func(kernel.InstanceSpec, []string) (kernel.InstanceSpec, error)) func(string) (Setting, error) {
	return func(value string) (Setting, error) {
		names := settingList(value)
		return Setting{Name: name,
			Apply: func(spec kernel.InstanceSpec, _ kernel.ReleaseBundle) (kernel.InstanceSpec, error) {
				return rule(spec, names)
			}}, nil
	}
}

// teamSetting is the one app-level change: it names no environment and reaches every one of the app's.
func teamSetting(value string) (Setting, error) {
	if value == "" {
		return Setting{}, fmt.Errorf("invalid: team: expected a team name")
	}
	return Setting{Name: "team", Team: value}, nil
}

// notModelled refuses a named setting v2 has no record field or document for.
func notModelled(name string) func(string) (Setting, error) {
	return func(string) (Setting, error) {
		return Setting{}, &Refusal{Subject: name, Reason: "not_modelled"}
	}
}

// settingInteger is one integer setting: its range is the record's own rule, so this only refuses what is not a
// number.
func settingInteger(name, value string) (int32, error) {
	count, err := strconv.ParseInt(value, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("invalid: %s: expected an integer", name)
	}
	return int32(count), nil
}

// settingList is a declaration list: names separated by commas, and an empty value clears the list.
func settingList(value string) []string {
	if value == "" {
		return []string{}
	}
	return strings.Split(value, ",")
}
