package forge_test

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/forge"
)

func TestIssueThread(t *testing.T) {
	t.Run("windows to the last 10 live comments, oldest first", func(t *testing.T) {
		f := forge.NewFake()
		f.SetIssue(forge.Issue{Number: "1", Title: "t", Body: "b"})
		var cs []forge.Comment
		for i := range 14 {
			cs = append(cs, forge.Comment{Author: "a", Body: fmt.Sprintf("c%02d", i)})
		}
		cs[13].Minimized = true
		f.CommentsFor = map[string][]forge.Comment{"1": cs}
		var warn bytes.Buffer

		th, err := forge.IssueThread(f, "1", &warn)
		iss, got := th.Issue, th.Comments
		if err != nil {
			t.Fatal(err)
		}
		if iss.Title != "t" || len(got) != 10 || got[0].Body != "c03" || got[9].Body != "c12" {
			t.Errorf("issue %+v, comments %+v: want the last 10 live comments c03..c12", iss, got)
		}
		if !strings.Contains(warn.String(), "dropped minimized") {
			t.Errorf("warn = %q, want a dropped-minimized line", warn.String())
		}
	})

	t.Run("a comments error is reported, not fatal", func(t *testing.T) {
		f := forge.NewFake()
		f.SetIssue(forge.Issue{Number: "1", Body: "b"})
		f.CommentsErr = map[string]error{"1": errors.New("boom")}
		var warn bytes.Buffer

		th, err := forge.IssueThread(f, "1", &warn)
		iss, got, cErr := th.Issue, th.Comments, th.CommentsErr
		if err != nil || iss.Body != "b" || len(got) != 0 {
			t.Errorf("got %+v, %+v, %v; want the issue, no comments, nil", iss, got, err)
		}
		if cErr == nil || !strings.Contains(warn.String(), "boom") {
			t.Errorf("comments error %v, warn %q; want the failure reported and logged", cErr, warn.String())
		}
	})

	t.Run("a numeric ref logs with a hash, a Jira key bare", func(t *testing.T) {
		for num, want := range map[string]string{"5": "?? #5: comments unreadable", "PROJ-5": "?? PROJ-5: comments unreadable"} {
			f := forge.NewFake()
			f.SetIssue(forge.Issue{Number: num, Body: "b"})
			f.CommentsErr = map[string]error{num: errors.New("boom")}
			var warn bytes.Buffer
			if _, err := forge.IssueThread(f, num, &warn); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(warn.String(), want) {
				t.Errorf("warn = %q, want %q", warn.String(), want)
			}
		}
	})

	t.Run("an issue error is returned", func(t *testing.T) {
		_, err := forge.IssueThread(forge.NewFake(), "9", io.Discard)
		if err == nil {
			t.Fatal("want the Issue error")
		}
	})

	t.Run("no comments for a tracker that is not a CommentLister", func(t *testing.T) {
		f := forge.NewFake()
		f.SetIssue(forge.Issue{Number: "1", Body: "b"})
		th, err := forge.IssueThread(issueOnlyTracker{IssueTracker: f}, "1", io.Discard)
		got := th.Comments
		if err != nil || len(got) != 0 {
			t.Errorf("comments %+v, err %v; want none", got, err)
		}
	})
}
