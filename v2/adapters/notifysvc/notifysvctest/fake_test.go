package notifysvctest

import (
	"github.com/samclement/swhurl-platform/v2/adapters/notifysvc"
	"testing"
)

func TestFakeContract(t *testing.T) {
	Contract(t, notifysvc.Request{App: "orders", Env: "prod", Key: "event:1", Title: "T", Message: "M", Priority: "high"}, func(t *testing.T, status int) (notifysvc.Sender, func() Sent) {
		f := &Fake{}
		if status == 0 {
			f.Fail = &notifysvc.Error{Reason: "unreachable"}
		} else if status != 202 {
			f.Fail = &notifysvc.Error{Reason: "refused"}
		}
		return f, func() Sent { return f.Sent[0] }
	})
}
