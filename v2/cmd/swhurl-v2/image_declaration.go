package main

import (
	"fmt"

	"github.com/samclement/swhurl-platform/v2/adapters/appsource"
	"github.com/samclement/swhurl-platform/v2/orchestration"
)

func (c cli) imageDeclaration(r request) int {
	reader := c.appSource
	if reader == nil {
		reader = appsource.Local{}
	}
	label, err := orchestration.ImageDeclaration(reader, r.appDir)
	if err != nil {
		fmt.Fprintln(c.diagnostic, err)
		return 2
	}
	return emit(c.out, c.diagnostic, []byte(label+"\n"))
}
