// Command forge is the fixture push-only forge Connector: it registers
// forge.RegisterBranches with PushOnly and translates the branch primitives to
// a forgejo-shaped REST backend over http_request, with Target.API as the base
// URL. The test supplies that URL because the scripted backend's port is
// dynamic. The backend scripts the hostile identities: Remote passes its
// clone_url and html_url through verbatim, so a credentialed or undeclared-host
// URL is the backend's to serve and core's to refuse.
package main

import (
	"encoding/json"
	"net/http"

	"spindrift.dev/sdk/connector"
	"spindrift.dev/sdk/forge"
)

func init() {
	forge.RegisterBranches(forge.Branches{
		Remote:       remote,
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

func remote(t forge.Target) (forge.Remote, error) {
	resp, err := call(http.MethodGet, t.API+"/repos/"+t.Repo)
	if err != nil {
		return forge.Remote{}, err
	}
	if resp.Status/100 != 2 {
		return forge.Remote{}, statusErr(resp.Status)
	}
	var repo struct {
		CloneURL string `json:"clone_url"`
		HTMLURL  string `json:"html_url"`
	}
	if err := json.Unmarshal(resp.Body, &repo); err != nil {
		return forge.Remote{}, err
	}
	return forge.Remote{CloneURL: repo.CloneURL, WebURL: repo.HTMLURL}, nil
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
