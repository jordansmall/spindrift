// Command forge is the fixture push-only forge Connector: it registers
// forge.RegisterBranches with PushOnly and translates the branch primitives to
// a forgejo-shaped REST backend over http_request, with Target.API as the base
// URL. The test supplies that URL because the scripted backend's port is
// dynamic. Link-time mode (-ldflags=-X=main.mode=tracker) also registers a
// tracker.Reader whose Issue reads the same backend, so one Connector serves
// both axes.
package main

import (
	"encoding/json"
	"net/http"

	"spindrift.dev/sdk/connector"
	"spindrift.dev/sdk/forge"
	"spindrift.dev/sdk/tracker"
)

var mode string

func init() {
	if mode == "tracker" {
		tracker.Register(tracker.Reader{ListIssues: listIssues, Issue: issue})
	}
	forge.RegisterBranches(forge.Branches{
		BranchExists: branchExists,
		DeleteBranch: deleteBranch,
		Probe:        probe,
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

// probe answers from the repository the backend serves, as a real forge's
// repo endpoint would.
func probe(t forge.Target) (string, error) {
	resp, err := call(http.MethodGet, t.API+"/repos/"+t.Repo)
	if err != nil {
		return "", err
	}
	if resp.Status != http.StatusOK {
		return "", statusErr(resp.Status)
	}
	var r struct {
		FullName string `json:"full_name"`
	}
	err = json.Unmarshal(resp.Body, &r)
	return r.FullName, err
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

// listIssues answers no issues: the dual-axis fixture is read by number only.
func listIssues(tracker.Target, tracker.Query, int) ([]tracker.Issue, error) { return nil, nil }

func issue(t tracker.Target, number string) (tracker.Issue, error) {
	resp, err := call(http.MethodGet, t.API+"/repos/"+t.Repo+"/issues/"+number)
	if err != nil {
		return tracker.Issue{}, err
	}
	if resp.Status != http.StatusOK {
		return tracker.Issue{}, statusErr(resp.Status)
	}
	var r struct {
		Title string `json:"title"`
		State string `json:"state"`
	}
	if err := json.Unmarshal(resp.Body, &r); err != nil {
		return tracker.Issue{}, err
	}
	return tracker.Issue{Number: number, Title: r.Title, State: r.State, Labels: []string{}}, nil
}

func main() {}
