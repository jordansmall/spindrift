// Command call is the fixture Connector for the generic call protocol: "echo"
// returns its params and "fail" returns an error.
package main

import (
	"encoding/json"
	"errors"

	"spindrift.dev/sdk/connector"
)

func init() {
	connector.Handle("echo", func(params json.RawMessage) (any, error) { return params, nil })
	connector.Handle("fail", func(json.RawMessage) (any, error) { return nil, errors.New("fixture failure") })
}

func main() {}
