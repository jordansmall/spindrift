package connectortracker

import (
	"errors"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/forge"
)

func commentsOf(t *testing.T, reply string) ([]forge.Comment, error) {
	t.Helper()
	call, _ := stubGuest(t, map[string]string{"tracker.comments": reply})
	tr := mustTracker(t, call, testLifecycle, testTarget, []string{CapComments}, true)
	cl, ok := forge.As[forge.CommentLister](tr)
	if !ok {
		t.Fatal("no CommentLister")
	}
	return cl.Comments("4")
}

func TestCommentsRefuseHostileFields(t *testing.T) {
	for _, tc := range []struct{ name, comment, field string }{
		{"empty author", `{"author":"","createdAt":"2024-01-02T03:04:05Z","body":"x"}`, "author"},
		{"newline author", `{"author":"a\nb","createdAt":"2024-01-02T03:04:05Z","body":"x"}`, "author"},
		{"delete author", `{"author":"a\u007f","createdAt":"2024-01-02T03:04:05Z","body":"x"}`, "author"},
		{"empty createdAt", `{"author":"a","createdAt":"","body":"x"}`, "createdAt"},
		{"missing createdAt", `{"author":"a","body":"x"}`, "createdAt"},
		{"garbage createdAt", `{"author":"a","createdAt":"yesterday","body":"x"}`, "createdAt"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := commentsOf(t, `{"comments":[`+tc.comment+`]}`)
			if !errors.Is(err, ErrAnswer) || !strings.Contains(err.Error(), tc.field) {
				t.Errorf("Comments = %+v, %v; want ErrAnswer naming %s", got, err, tc.field)
			}
		})
	}
}

func TestCommentsUnknownAssociationHasNoStanding(t *testing.T) {
	got, err := commentsOf(t, `{"comments":[
		{"author":"a","createdAt":"2024-01-02T03:04:05.5Z","body":"x","association":"OWNER"},
		{"author":"b","createdAt":"2024-01-02T03:04:05Z","body":"y","association":"SUPERUSER"},
		{"author":"c","createdAt":"2024-01-02T03:04:05Z","body":"z","association":"owner"},
		{"author":"d","createdAt":"2024-01-02T03:04:05Z","body":"w","association":"NONE"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	want := []struct {
		assoc   string
		trusted bool
	}{{"OWNER", true}, {"", false}, {"", false}, {"NONE", false}}
	for i, w := range want {
		if got[i].Association != w.assoc || forge.CommentTrusted(got[i]) != w.trusted {
			t.Errorf("comment %d = %+v, want association %q trusted %v", i, got[i], w.assoc, w.trusted)
		}
	}
}

var hostileLabels = map[string]string{
	"empty":   `""`,
	"newline": `"a\nb"`,
	"delete":  `"a\u007f"`,
}

func TestIssueReadRefusesHostileLabels(t *testing.T) {
	for name, label := range hostileLabels {
		t.Run(name, func(t *testing.T) {
			body := `{"number":"1","state":"open","labels":["ok",` + label + `]}`
			call, _ := stubGuest(t, map[string]string{
				"tracker.issue":      body,
				"tracker.listIssues": `{"issues":[` + body + `]}`,
			})
			tr := mustTracker(t, call, testLifecycle, testTarget, nil, true)
			if _, err := tr.Issue("1"); !errors.Is(err, ErrAnswer) || !strings.Contains(err.Error(), "label") {
				t.Errorf("Issue err = %v, want ErrAnswer naming label", err)
			}
			if _, err := tr.ListOpenIssues(); !errors.Is(err, ErrAnswer) {
				t.Errorf("ListOpenIssues err = %v, want ErrAnswer", err)
			}
		})
	}
}

func TestListLabelsRefusesHostileNames(t *testing.T) {
	for name, label := range hostileLabels {
		t.Run(name, func(t *testing.T) {
			call, _ := stubGuest(t, map[string]string{"tracker.listLabels": `{"labels":["ok",` + label + `]}`})
			tr := mustTracker(t, call, testLifecycle, testTarget, []string{CapLabels}, true)
			if got, err := tr.ListLabels(); !errors.Is(err, ErrAnswer) || !strings.Contains(err.Error(), "label") {
				t.Errorf("ListLabels = %v, %v; want ErrAnswer naming label", got, err)
			}
		})
	}
}
