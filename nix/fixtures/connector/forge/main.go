// Command forge is the fixture push-only forge Connector: it registers
// forge.RegisterBranches with PushOnly and translates the branch primitives to
// a forgejo-shaped REST backend over http_request, with Target.API as the base
// URL. The test supplies that URL because the scripted backend's port is
// dynamic.
package main

import (
	"net/http"

	"spindrift.dev/sdk/connector"
	"spindrift.dev/sdk/forge"
)

func init() {
	forge.RegisterBranches(forge.Branches{
		BranchExists: branchExists,
		DeleteBranch: deleteBranch,
		PushOnly:     true,
	})
}

// call sends one request to the backend and returns its status and body. A
// transport failure (a dropped connection) is transient: unclassed, it would
// read as permanent and core would never retry it.
func call(method, url string) (connector.Response, error) {
	resp, err := connector.HTTPRequest(connector.Request{Method: method, URL: url})
	if err != nil {
		return connector.Response{}, connector.Errorf(connector.Transient, "forge backend unreachable: %w", err)
	}
	return resp, nil
}

func statusErr(status int) error {
	return connector.Errorf(connector.ClassForStatus(status), "forge backend answered status %d", status)
}

// The branch rides in the path raw: the host refuses an escaped path.
func branchExists(t forge.Target, branch string) (bool, error) {
	resp, err := call(http.MethodGet, t.API+"/repos/"+t.Repo+"/branches/"+branch)
	if err != nil {
		return false, err
	}
	switch resp.Status {
	case http.StatusOK:
		return true, nil
	case http.StatusNotFound:
		return false, nil
	}
	return false, statusErr(resp.Status)
}

func deleteBranch(t forge.Target, branch string) error {
	resp, err := call(http.MethodDelete, t.API+"/repos/"+t.Repo+"/branches/"+branch)
	if err != nil {
		return err
	}
	if resp.Status != http.StatusNoContent && resp.Status != http.StatusOK {
		return statusErr(resp.Status)
	}
	return nil
}

func main() {}
