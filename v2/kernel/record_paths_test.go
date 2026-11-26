package kernel

import "testing"

// A path names an instance only as the instance record two folders below the root; every path the writer builds
// for that instance reads back as it.
func TestInstanceRecordOf(t *testing.T) {
	cases := []struct {
		file, application, environment string
		ok                             bool
	}{
		{InstanceRecordPath("v2-records", "web", "staging"), "web", "staging", true},
		{"v2-records/web/staging/release.json", "", "", false},
		{AppRecordPath("v2-records", "web"), "", "", false},
		{"v2-records/web/staging/offers/instance.json", "", "", false},
		{"v2-records/web/instance.json", "", "", false},
		{"v2-records//staging/instance.json", "", "", false},
		{"other/web/staging/instance.json", "", "", false},
		{"v2-records-old/web/staging/instance.json", "", "", false},
	}
	for _, c := range cases {
		application, environment, ok := InstanceRecordOf("v2-records", c.file)
		if application != c.application || environment != c.environment || ok != c.ok {
			t.Errorf("%s = %q %q %v, want %q %q %v", c.file, application, environment, ok, c.application,
				c.environment, c.ok)
		}
	}
	if got := ReleaseRecordPath("v2-records", "web", "staging"); got != "v2-records/web/staging/release.json" {
		t.Errorf("release record = %s", got)
	}
}
