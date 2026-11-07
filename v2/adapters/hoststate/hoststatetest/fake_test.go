package hoststatetest_test

import (
	"os"
	"testing"

	"github.com/samclement/swhurl-platform/v2/adapters/hoststate"
	"github.com/samclement/swhurl-platform/v2/adapters/hoststate/hoststatetest"
)

func TestContract(t *testing.T) {
	held, err := os.ReadFile("../testdata/notification-heartbeat.json")
	if err != nil {
		t.Fatal(err)
	}
	scenario := hoststatetest.Scenario{Existing: "/state/notification-heartbeat.json", Held: held,
		Absent: "/new/dir/state.json", Unreadable: "/state/unreadable", Unwritable: "/state/unwritable"}
	hoststatetest.Contract(t, scenario, func(t *testing.T, s hoststatetest.Scenario) hoststate.Store {
		fake := hoststatetest.New()
		fake.Files[s.Existing] = s.Held
		fake.Unreadable[s.Unreadable] = true
		fake.Unwritable[s.Unwritable] = true
		return fake
	})
}

// The fake records each read and replacement, and keeps its own copies.
func TestFakeRecordsAndCopies(t *testing.T) {
	fake := hoststatetest.New()
	data := []byte("one\n")
	if err := fake.Replace("/s", data); err != nil {
		t.Fatal(err)
	}
	data[0] = 'X'
	read, _, _ := fake.Read("/s")
	read[1] = 'X'
	if again, _, _ := fake.Read("/s"); string(again) != "one\n" || len(fake.Reads) != 2 || len(fake.Replaced) != 1 {
		t.Fatalf("%q %v %v", again, fake.Reads, fake.Replaced)
	}
}
