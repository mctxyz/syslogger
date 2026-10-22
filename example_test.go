package syslogger_test

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"

	"github.com/mctxyz/syslogger"
)

func ExampleComponent_Unhealthy() {
	manager, err := syslogger.New(syslogger.Config{Project: "demo", StateEnabled: true, HTTP: syslogger.HTTPConfig{Enabled: true}})
	if err != nil {
		panic(err)
	}
	defer manager.Close(context.Background())
	rpc, err := manager.Register("rpc", 512)
	if err != nil {
		panic(err)
	}
	rpc.Unhealthy()
	handler, err := manager.Handler()
	if err != nil {
		panic(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest("GET", "/metrics", nil))
	fmt.Println(strings.Contains(response.Body.String(), `syslogger_component_state{project="demo",instance_id="common",component="rpc"} 3`))
	// Output: true
}
